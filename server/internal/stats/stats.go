// Package stats keeps a history of requests and heartbeats in TimescaleDB, and serves usage analytics.
// Only central connects to the database. Rows hold metadata only: no prompt text and no response text.
package stats

import (
	"bytes"
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgxpool"

	"mooch-serv/internal/feed"
	"mooch-serv/internal/registry"
)

//go:embed schema.sql
var schema string

const (
	queueSize  = 1024
	batchSize  = 200
	flushEvery = time.Second
	loadEvery  = 15 * time.Second
)

// Request is one routed request. Token counts are nil when the backend does not report usage.
type Request struct {
	Time             time.Time
	Requester        string
	Node             string
	Model            string
	Path             string
	Status           int
	Duration         time.Duration
	BytesOut         int64
	PromptTokens     *int
	CompletionTokens *int
}

type beat struct {
	time   time.Time
	node   string
	models int
}

// Store writes events to the database in batches. A nil Store records nothing.
type Store struct {
	pool    *pgxpool.Pool
	sendMu  sync.RWMutex // Close takes the write lock, so no send goes to a closed channel
	closed  bool
	events  chan any
	done    chan struct{}
	dropped atomic.Int64 // rows lost to a full queue or to a failed write
	flush   func(ctx context.Context, reqs []Request, beats []beat) error
	log     *slog.Logger

	loadMu sync.RWMutex
	load   map[string]float64 // milliseconds of work that each node served in the last hour
}

// Open connects to url, applies the schema, and starts the batch writer.
func Open(ctx context.Context, url string) (*Store, error) {
	pool, err := pgxpool.New(ctx, url)
	if err != nil {
		return nil, err
	}
	if err := migrate(ctx, pool); err != nil {
		pool.Close()
		return nil, err
	}
	s := newStore(nil)
	s.pool = pool
	s.flush = s.copy
	return s, nil
}

// newStore returns a Store with a running batch writer. Tests give their own flush function.
func newStore(flush func(context.Context, []Request, []beat) error) *Store {
	s := &Store{
		events: make(chan any, queueSize),
		done:   make(chan struct{}),
		flush:  flush,
		log:    slog.With("component", "stats"),
		load:   map[string]float64{},
	}
	go s.write()
	return s
}

func migrate(ctx context.Context, pool *pgxpool.Pool) error {
	for _, block := range strings.Split(schema, "\n\n") {
		if isComment(block) {
			continue
		}
		if _, err := pool.Exec(ctx, block); err != nil {
			return fmt.Errorf("apply schema: %w\n%s", err, block)
		}
	}
	return nil
}

func isComment(block string) bool {
	for _, line := range strings.Split(block, "\n") {
		if l := strings.TrimSpace(line); l != "" && !strings.HasPrefix(l, "--") {
			return false
		}
	}
	return true
}

// Record queues a request row and adds its work to the load of the node at once.
// When the queue is full, Record drops the row, so a slow database never blocks a request.
func (s *Store) Record(r Request) {
	if s == nil {
		return
	}
	s.loadMu.Lock()
	s.load[r.Node] += float64(r.Duration.Milliseconds())
	s.loadMu.Unlock()
	s.enqueue(r)
}

// Heartbeat queues a heartbeat row for node.
func (s *Store) Heartbeat(n registry.Node) {
	if s == nil {
		return
	}
	s.enqueue(beat{time.Now(), feed.Name(n), len(feed.Models(n))})
}

func (s *Store) enqueue(e any) {
	s.sendMu.RLock()
	defer s.sendMu.RUnlock()
	if s.closed {
		return
	}
	select {
	case s.events <- e:
	default:
		s.dropped.Add(1)
	}
}

// Load returns the milliseconds of work that node served in the last hour.
func (s *Store) Load(n registry.Node) float64 {
	if s == nil {
		return 0
	}
	s.loadMu.RLock()
	defer s.loadMu.RUnlock()
	return s.load[feed.Name(n)]
}

// Close writes the queued rows and closes the database. Rows that come after Close are dropped.
func (s *Store) Close() {
	if s == nil {
		return
	}
	s.sendMu.Lock()
	s.closed = true
	close(s.events)
	s.sendMu.Unlock()
	<-s.done
	if s.pool != nil {
		s.pool.Close()
	}
}

// write sends the queued rows in batches: every flushEvery, or when batchSize rows are ready.
func (s *Store) write() {
	defer close(s.done)
	ticker := time.NewTicker(flushEvery)
	defer ticker.Stop()
	var reqs []Request
	var beats []beat
	failing := false
	send := func() {
		if len(reqs)+len(beats) == 0 {
			return
		}
		ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		err := s.flush(ctx, reqs, beats)
		cancel()
		if err != nil {
			s.dropped.Add(int64(len(reqs) + len(beats)))
		}
		// Log only a change of state, so a stopped database does not fill the console.
		if err != nil && !failing {
			s.log.Warn("analytics write failed, rows are dropped until the database recovers", "error", err)
		} else if err == nil && failing {
			s.log.Warn("analytics write recovered", "dropped", s.dropped.Load())
		}
		failing = err != nil
		reqs, beats = reqs[:0], beats[:0]
	}
	for {
		select {
		case e, ok := <-s.events:
			if !ok {
				send()
				return
			}
			switch e := e.(type) {
			case Request:
				reqs = append(reqs, e)
			case beat:
				beats = append(beats, e)
			}
			if len(reqs)+len(beats) >= batchSize {
				send()
			}
		case <-ticker.C:
			send()
		}
	}
}

func (s *Store) copy(ctx context.Context, reqs []Request, beats []beat) error {
	if len(reqs) > 0 {
		_, err := s.pool.CopyFrom(ctx, pgx.Identifier{"requests"},
			[]string{"time", "requester", "node", "model", "path", "status", "duration_ms", "bytes_out", "prompt_tokens", "completion_tokens"},
			pgx.CopyFromSlice(len(reqs), func(i int) ([]any, error) {
				r := reqs[i]
				return []any{r.Time, r.Requester, r.Node, r.Model, r.Path, r.Status,
					float64(r.Duration.Microseconds()) / 1000, r.BytesOut, r.PromptTokens, r.CompletionTokens}, nil
			}))
		if err != nil {
			return err
		}
	}
	if len(beats) > 0 {
		_, err := s.pool.CopyFrom(ctx, pgx.Identifier{"heartbeats"}, []string{"time", "node", "models"},
			pgx.CopyFromSlice(len(beats), func(i int) ([]any, error) {
				return []any{beats[i].time, beats[i].node, beats[i].models}, nil
			}))
		if err != nil {
			return err
		}
	}
	return nil
}

// RefreshLoad calls refreshLoad every loadEvery, until ctx ends.
// ponytail: the load changes only when a request ends, so a burst of parallel requests can go to one node.
// Count requests in flight if bursts matter.
func (s *Store) RefreshLoad(ctx context.Context) {
	if s == nil || s.pool == nil {
		return
	}
	ticker := time.NewTicker(loadEvery)
	defer ticker.Stop()
	for {
		if err := s.refreshLoad(ctx); err != nil {
			s.log.Debug("load refresh failed", "error", err)
		}
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
	}
}

// refreshLoad reads the work that each node served in the last hour from the database.
func (s *Store) refreshLoad(ctx context.Context) error {
	load := map[string]float64{}
	rows, _ := s.pool.Query(ctx, "SELECT node, sum(duration_ms) FROM requests WHERE time > now() - INTERVAL '1 hour' GROUP BY node")
	var node string
	var ms float64
	if _, err := pgx.ForEachRow(rows, []any{&node, &ms}, func() error { load[node] = ms; return nil }); err != nil {
		return err
	}
	s.loadMu.Lock()
	s.load = load
	s.loadMu.Unlock()
	return nil
}

// Usage reads the token counts of an OpenAI response body: plain JSON, or the last
// server-sent event that has a usage field. It returns nil values when the body has no usage.
func Usage(body []byte) (prompt, completion *int) {
	type usage struct {
		Usage *struct {
			Prompt     int `json:"prompt_tokens"`
			Completion int `json:"completion_tokens"`
		} `json:"usage"`
	}
	chunks := [][]byte{body}
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("data:")) {
		chunks = nil
		for _, line := range bytes.Split(body, []byte("\n")) {
			if data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:")); ok {
				chunks = append(chunks, data)
			}
		}
	}
	for i := len(chunks) - 1; i >= 0; i-- {
		var u usage
		if json.Unmarshal(chunks[i], &u) == nil && u.Usage != nil {
			return &u.Usage.Prompt, &u.Usage.Completion
		}
	}
	return nil, nil
}
