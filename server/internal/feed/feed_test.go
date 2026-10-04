package feed

import (
	"bytes"
	"strings"
	"testing"
	"time"

	"mooch-serv/internal/registry"
)

func TestFeedLines(t *testing.T) {
	var out bytes.Buffer
	f := New(&out)
	f.now = func() time.Time { return time.Date(2026, 1, 1, 9, 5, 0, 0, time.UTC) }
	f.Joined(registry.Node{NodeID: "n1", Name: "gpu-box", TailscaleIP: "100.64.0.7",
		Services: []registry.Service{{Models: []string{"a", "b"}}}})
	f.Left(registry.Node{NodeID: "n2"})
	id := f.Route("laptop", "a", []string{"gpu-box"}, "gpu-box")
	f.Request(id, "laptop", "gpu-box", "POST", "/v1/chat/completions", "hi")
	f.Response(id, "gpu-box", "laptop", 200, 1234*time.Millisecond, "")
	want := `09:05:00  JOIN  gpu-box 100.64.0.7 · models: a, b
09:05:00  LEAVE n2 no heartbeat
          ╭────────────────────────────────────────────────────────────
09:05:00  │ ROUTE #1 laptop asks for a → gpu-box · served by: gpu-box
          ╰────────────────────────────────────────────────────────────
          ╭────────────────────────────────────────────────────────────
09:05:00  │ REQ   [ laptop → gpu-box ] POST "hi" /v1/chat/completions · #1
          ╰────────────────────────────────────────────────────────────
          ╭────────────────────────────────────────────────────────────
09:05:00  │ RESP  [ gpu-box → laptop ] 200 (no text) in 1.234s · #1
          ╰────────────────────────────────────────────────────────────
`
	if out.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", out.String(), want)
	}
	var nilFeed *Feed
	nilFeed.Joined(registry.Node{}) // A nil feed must not panic.
	nilFeed.Response(nilFeed.Route("", "", nil, ""), "", "", 0, 0, "")
}

func TestResponsePreview(t *testing.T) {
	cases := map[string]string{
		`{"choices":[{"message":{"content":"hello  there"}}]}`: "hello there",
		`{"choices":[{"text":"done"}]}`:                        "done",
		"data: {\"choices\":[{\"delta\":{\"content\":\"Hel\"}}]}\n\ndata: {\"choices\":[{\"delta\":{\"content\":\"lo\"}}]}\n\ndata: [DONE]\n": "Hello",
		`{"error":{"message":"model is busy"}}`: "error: model is busy",
		`{"data":[{"embedding":[0.1]}]}`:        "",
	}
	for body, want := range cases {
		if got := ResponsePreview([]byte(body)); got != want {
			t.Errorf("ResponsePreview(%.40q) = %q, want %q", body, got, want)
		}
	}
}

func TestPreview(t *testing.T) {
	long := strings.Repeat("x", 200)
	cases := map[string]string{
		`{"messages":[{"role":"user","content":"first"},{"role":"assistant","content":"a"},{"role":"user","content":"  last \n one "}]}`: "last one",
		`{"messages":[{"role":"user","content":[{"type":"text","text":"hi"},{"type":"image_url"}]}]}`:                                    "hi",
		`{"prompt":"plain prompt"}`: "plain prompt",
		`{"prompt":"` + long + `"}`: strings.Repeat("x", previewLen) + "…",
		`not json`:                  "",
	}
	for body, want := range cases {
		if got := Preview([]byte(body)); got != want {
			t.Errorf("Preview(%.40q) = %q, want %q", body, got, want)
		}
	}
}

func TestBanner(t *testing.T) {
	var out bytes.Buffer
	New(&out).Banner("100.64.0.1:8080")
	want := `
╭────────────────────────────────╮
│  MOOCH.AI HAS STARTED          │
│  listening on 100.64.0.1:8080  │
╰────────────────────────────────╯
`
	if out.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", out.String(), want)
	}
}
