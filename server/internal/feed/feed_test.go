package feed

import (
	"bytes"
	"strings"
	"testing"

	"peerai-serv/internal/registry"
)

func TestFeedLines(t *testing.T) {
	var out bytes.Buffer
	f := New(&out)
	f.Joined(registry.Node{NodeID: "n1", Name: "gpu-box", TailscaleIP: "100.64.0.7",
		Services: []registry.Service{{Models: []string{"a", "b"}}}})
	f.Left(registry.Node{NodeID: "n2"})
	want := "-> gpu-box has joined the cluster (100.64.0.7, 2 models)\n<- n2 has left the cluster (no heartbeat)\n"
	if out.String() != want {
		t.Fatalf("got %q, want %q", out.String(), want)
	}
	var nilFeed *Feed
	nilFeed.Joined(registry.Node{}) // A nil feed must not panic.
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
│  PEER AI HAS STARTED           │
│  listening on 100.64.0.1:8080  │
╰────────────────────────────────╯
`
	if out.String() != want {
		t.Fatalf("got\n%s\nwant\n%s", out.String(), want)
	}
}
