package stats

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"

	"mooch-central/internal/registry"
)

// openTestDB makes a new database on the server at MOOCH_TEST_DB_URL and drops it after the test.
// The test does not touch the data in the main database.
func openTestDB(t *testing.T) *Store {
	t.Helper()
	base := os.Getenv("MOOCH_TEST_DB_URL")
	if base == "" {
		t.Skip("set MOOCH_TEST_DB_URL to run the database tests, for example with make test-db")
	}
	ctx := context.Background()
	admin, err := pgx.Connect(ctx, base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { admin.Close(ctx) })
	name := fmt.Sprintf("mooch_test_%d", time.Now().UnixNano())
	if _, err := admin.Exec(ctx, "CREATE DATABASE "+name); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _, _ = admin.Exec(ctx, "DROP DATABASE "+name+" WITH (FORCE)") })
	u, _ := url.Parse(base)
	u.Path = "/" + name
	s, err := Open(ctx, u.String())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	return s
}

func TestDBSummaryAndBalance(t *testing.T) {
	s := openTestDB(t)
	ctx := context.Background()
	now := time.Now().Add(-10 * time.Minute)
	req := func(from, to string, secs, status, prompt, completion int) Request {
		return Request{Time: now, Requester: from, Node: to, Model: "qwen", Path: "/v1/chat/completions",
			Status: status, Duration: time.Duration(secs) * time.Second, PromptTokens: &prompt, CompletionTokens: &completion}
	}
	noUsage := req("laptop", "gpu-a", 2, 200, 0, 0)
	noUsage.PromptTokens, noUsage.CompletionTokens = nil, nil
	reqs := []Request{req("laptop", "gpu-a", 2, 200, 10, 90), noUsage, req("laptop", "gpu-a", 2, 502, 0, 0),
		req("gpu-b", "gpu-a", 1, 200, 20, 30), req("gpu-a", "gpu-b", 4, 200, 100, 200)}
	beats := []beat{{now, "gpu-a", 1}, {now.Add(time.Minute), "gpu-a", 1}, {now.Add(2 * time.Minute), "gpu-a", 1}}
	if err := s.copy(ctx, reqs, beats); err != nil {
		t.Fatal(err)
	}

	v, err := s.summary(ctx, "1h", windows["1h"])
	if err != nil {
		t.Fatal(err)
	}
	sum := v.(Summary)
	by := map[string]NodeUsage{}
	for _, n := range sum.Nodes {
		by[n.Name] = n
	}
	a, b, laptop := by["gpu-a"], by["gpu-b"], by["laptop"]
	// gpu-a serves 100 + 0 + 0 + 50 tokens and uses 300 tokens.
	if a.Served != 4 || a.TokensServed != 150 || a.Used != 1 || a.TokensUsed != 300 || a.BalanceTokens != -150 || a.OKPct != 75 {
		t.Fatalf("gpu-a = %+v", a)
	}
	if b.BalanceTokens != 250 || laptop.BalanceTokens != -100 || laptop.Used != 3 || laptop.TokensUsed != 100 {
		t.Fatalf("gpu-b = %+v, laptop = %+v", b, laptop)
	}
	if a.UptimeMin != 3 || a.P50MS != 2000 {
		t.Fatalf("gpu-a uptime %d min, p50 %v ms", a.UptimeMin, a.P50MS)
	}
	if sum.Requests != 5 || sum.Tokens != 450 || sum.Nodes[0].Name != "gpu-b" {
		t.Fatalf("summary = %+v", sum)
	}

	m, err := s.models(ctx, "24h", windows["24h"])
	if err != nil {
		t.Fatal(err)
	}
	if got := m.(map[string]any)["models"].([]ModelUsage); len(got) != 1 || got[0].Requests != 5 || got[0].Tokens != 450 || got[0].Nodes != 2 {
		t.Fatalf("models = %+v", got)
	}

	if err := s.refreshLoad(ctx); err != nil {
		t.Fatal(err)
	}
	if got := s.Load(registry.Node{NodeID: "gpu-a"}); got != 7000 {
		t.Fatalf("load of gpu-a = %v ms, want 7000", got)
	}

	mux := http.NewServeMux()
	s.Register(mux)
	for _, path := range []string{"/api/analytics/summary?window=7d", "/api/analytics/models?window=30d"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest("GET", path, nil))
		if rec.Code != http.StatusOK {
			t.Fatalf("GET %s = %d %s", path, rec.Code, rec.Body)
		}
	}
}

// TestDBSchemaAppliesTwice checks that a restart of central can apply the schema again.
func TestDBSchemaAppliesTwice(t *testing.T) {
	s := openTestDB(t)
	if err := migrate(context.Background(), s.pool); err != nil {
		t.Fatal(err)
	}
}
