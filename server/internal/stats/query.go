package stats

import (
	"context"
	_ "embed"
	"math"
	"net/http"
	"sort"
	"time"

	"github.com/jackc/pgx/v5"

	"mooch-serv/internal/respond"
)

//go:embed page.html
var page string

// window is a time range that the analytics accept. bucket is the width of one point in a time series.
type window struct {
	span   time.Duration
	bucket time.Duration
}

var windows = map[string]window{
	"1h":  {time.Hour, 5 * time.Minute},
	"24h": {24 * time.Hour, time.Hour},
	"7d":  {7 * 24 * time.Hour, 6 * time.Hour},
	"30d": {30 * 24 * time.Hour, 24 * time.Hour},
}

// parseWindow reads the window query parameter. An empty value means 24h.
func parseWindow(r *http.Request) (string, window, bool) {
	name := r.URL.Query().Get("window")
	if name == "" {
		name = "24h"
	}
	w, ok := windows[name]
	return name, w, ok
}

// Register adds the analytics routes. The routes return 503 when the Store is nil.
func (s *Store) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /analytics", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		_, _ = w.Write([]byte(page))
	})
	mux.HandleFunc("GET /api/analytics/summary", s.handle(s.summary))
	mux.HandleFunc("GET /api/analytics/timeseries", s.handle(s.timeseries))
	mux.HandleFunc("GET /api/analytics/models", s.handle(s.models))
	mux.HandleFunc("GET /leaderboard.json", s.handleLeaderboard)
}

type query func(ctx context.Context, name string, w window) (any, error)

func (s *Store) handle(q query) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if s == nil || s.pool == nil {
			respond.Error(w, http.StatusServiceUnavailable, "analytics are off: set MOOCH_DB_URL on central")
			return
		}
		name, win, ok := parseWindow(r)
		if !ok {
			respond.Error(w, http.StatusBadRequest, "window must be 1h, 24h, 7d, or 30d")
			return
		}
		v, err := q(r.Context(), name, win)
		if err != nil {
			s.log.Warn("analytics query failed", "error", err)
			respond.Error(w, http.StatusServiceUnavailable, "analytics database is unavailable")
			return
		}
		respond.JSON(w, v)
	}
}

// NodeUsage is the give and take of one participant. A participant is a node, or a tool at an IP that is not a node.
type NodeUsage struct {
	Name         string  `json:"name"`
	Served       int64   `json:"served"`
	ServedS      float64 `json:"served_s"`
	Used         int64   `json:"used"`
	UsedS        float64 `json:"used_s"`
	BalanceS     float64 `json:"balance_s"`
	TokensServed int64   `json:"tokens_served"`
	OKPct        float64 `json:"ok_pct"`
	P50MS        float64 `json:"p50_ms"`
	UptimeMin    int64   `json:"uptime_min"`
	UptimePct    float64 `json:"uptime_pct"`
	Models       int64   `json:"models"`
}

type Summary struct {
	Window    string      `json:"window"`
	UpdatedAt time.Time   `json:"updated_at"`
	Requests  int64       `json:"requests"`
	ComputeS  float64     `json:"compute_s"`
	Tokens    int64       `json:"tokens"`
	Nodes     []NodeUsage `json:"nodes"`
}

func (s *Store) summary(ctx context.Context, name string, w window) (any, error) {
	since := time.Now().Add(-w.span)
	by := map[string]*NodeUsage{}
	row := func(name string) *NodeUsage {
		if by[name] == nil {
			by[name] = &NodeUsage{Name: name}
		}
		return by[name]
	}
	// The hourly aggregate gives the totals. Its first bucket can start up to one hour before since.
	rows, _ := s.pool.Query(ctx, `
		SELECT node, sum(requests), sum(ok), sum(duration_ms) / 1000, sum(tokens), count(DISTINCT model)
		FROM usage_hourly WHERE bucket >= time_bucket(INTERVAL '1 hour', $1::timestamptz) GROUP BY node`, since)
	var node string
	var n, ok, tokens, models int64
	var secs float64
	if _, err := pgx.ForEachRow(rows, []any{&node, &n, &ok, &secs, &tokens, &models}, func() error {
		u := row(node)
		u.Served, u.ServedS, u.TokensServed, u.Models = n, secs, tokens, models
		if n > 0 {
			u.OKPct = round(float64(ok) * 100 / float64(n))
		}
		return nil
	}); err != nil {
		return nil, err
	}
	rows, _ = s.pool.Query(ctx, `
		SELECT requester, sum(requests), sum(duration_ms) / 1000
		FROM usage_hourly WHERE bucket >= time_bucket(INTERVAL '1 hour', $1::timestamptz) GROUP BY requester`, since)
	if _, err := pgx.ForEachRow(rows, []any{&node, &n, &secs}, func() error {
		u := row(node)
		u.Used, u.UsedS = n, secs
		return nil
	}); err != nil {
		return nil, err
	}
	// The median needs the raw rows, because a continuous aggregate cannot keep an ordered-set aggregate.
	rows, _ = s.pool.Query(ctx, `
		SELECT node, percentile_cont(0.5) WITHIN GROUP (ORDER BY duration_ms)
		FROM requests WHERE time >= $1 AND status < 400 GROUP BY node`, since)
	var p50 float64
	if _, err := pgx.ForEachRow(rows, []any{&node, &p50}, func() error {
		row(node).P50MS = math.Round(p50)
		return nil
	}); err != nil {
		return nil, err
	}
	// Uptime is the number of minutes that have at least one heartbeat.
	rows, _ = s.pool.Query(ctx, `
		SELECT node, count(DISTINCT time_bucket(INTERVAL '1 minute', time))
		FROM heartbeats WHERE time >= $1 GROUP BY node`, since)
	var mins int64
	if _, err := pgx.ForEachRow(rows, []any{&node, &mins}, func() error {
		u := row(node)
		u.UptimeMin = mins
		u.UptimePct = round(min(100, float64(mins)*100/w.span.Minutes()))
		return nil
	}); err != nil {
		return nil, err
	}

	sum := Summary{Window: name, UpdatedAt: time.Now().UTC(), Nodes: []NodeUsage{}}
	for _, u := range by {
		u.ServedS, u.UsedS = round(u.ServedS), round(u.UsedS)
		u.BalanceS = round(u.ServedS - u.UsedS)
		sum.Requests += u.Served
		sum.ComputeS += u.ServedS
		sum.Tokens += u.TokensServed
		sum.Nodes = append(sum.Nodes, *u)
	}
	sum.ComputeS = round(sum.ComputeS)
	sort.Slice(sum.Nodes, func(i, j int) bool {
		if sum.Nodes[i].BalanceS != sum.Nodes[j].BalanceS {
			return sum.Nodes[i].BalanceS > sum.Nodes[j].BalanceS
		}
		return sum.Nodes[i].Name < sum.Nodes[j].Name
	})
	return sum, nil
}

// Point is the work that one node served in one time bucket.
type Point struct {
	Time     time.Time `json:"time"`
	Node     string    `json:"node"`
	Requests int64     `json:"requests"`
	ComputeS float64   `json:"compute_s"`
}

func (s *Store) timeseries(ctx context.Context, name string, w window) (any, error) {
	since := time.Now().Add(-w.span)
	// Buckets of one hour or more come from the aggregate. Smaller buckets need the raw rows.
	sql := `SELECT time_bucket($2::interval, bucket) AS b, node, sum(requests), sum(duration_ms) / 1000
		FROM usage_hourly WHERE bucket >= $1 GROUP BY b, node ORDER BY b, node`
	if w.bucket < time.Hour {
		sql = `SELECT time_bucket($2::interval, time) AS b, node, count(*), sum(duration_ms) / 1000
			FROM requests WHERE time >= $1 GROUP BY b, node ORDER BY b, node`
	}
	rows, _ := s.pool.Query(ctx, sql, since, w.bucket)
	points := []Point{}
	var p Point
	_, err := pgx.ForEachRow(rows, []any{&p.Time, &p.Node, &p.Requests, &p.ComputeS}, func() error {
		p.ComputeS = round(p.ComputeS)
		points = append(points, p)
		return nil
	})
	return map[string]any{"window": name, "bucket_s": w.bucket.Seconds(), "points": points}, err
}

// ModelUsage is the use of one model across the cluster.
type ModelUsage struct {
	Model    string  `json:"model"`
	Requests int64   `json:"requests"`
	ComputeS float64 `json:"compute_s"`
	Tokens   int64   `json:"tokens"`
	Nodes    int64   `json:"nodes"`
}

func (s *Store) models(ctx context.Context, name string, w window) (any, error) {
	rows, _ := s.pool.Query(ctx, `
		SELECT model, sum(requests), sum(duration_ms) / 1000, sum(tokens), count(DISTINCT node)
		FROM usage_hourly WHERE bucket >= time_bucket(INTERVAL '1 hour', $1::timestamptz)
		GROUP BY model ORDER BY sum(requests) DESC`, time.Now().Add(-w.span))
	out := []ModelUsage{}
	var m ModelUsage
	_, err := pgx.ForEachRow(rows, []any{&m.Model, &m.Requests, &m.ComputeS, &m.Tokens, &m.Nodes}, func() error {
		m.ComputeS = round(m.ComputeS)
		out = append(out, m)
		return nil
	})
	return map[string]any{"window": name, "models": out}, err
}

// handleLeaderboard serves the last 24 hours in the shape of frontend/public/leaderboard.example.json.
func (s *Store) handleLeaderboard(w http.ResponseWriter, r *http.Request) {
	if s == nil || s.pool == nil {
		respond.Error(w, http.StatusServiceUnavailable, "analytics are off: set MOOCH_DB_URL on central")
		return
	}
	v, err := s.summary(r.Context(), "24h", windows["24h"])
	if err != nil {
		s.log.Warn("analytics query failed", "error", err)
		respond.Error(w, http.StatusServiceUnavailable, "analytics database is unavailable")
		return
	}
	respond.JSON(w, Leaderboard(v.(Summary)))
}

type LeaderboardEntry struct {
	Alias  string  `json:"alias"`
	Models int64   `json:"models"`
	Served int64   `json:"served"`
	OKPct  float64 `json:"ok_pct"`
	P50MS  float64 `json:"p50_ms"`
	Uptime string  `json:"uptime"`
}

// Leaderboard ranks the participants that served requests by the number of requests served.
func Leaderboard(sum Summary) map[string]any {
	entries := []LeaderboardEntry{}
	for _, u := range sum.Nodes {
		if u.Served == 0 {
			continue
		}
		entries = append(entries, LeaderboardEntry{u.Name, u.Models, u.Served, u.OKPct, u.P50MS,
			(time.Duration(u.UptimeMin) * time.Minute).String()})
	}
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Served > entries[j].Served })
	return map[string]any{"updated_at": sum.UpdatedAt, "entries": entries}
}

func round(v float64) float64 { return math.Round(v*10) / 10 }
