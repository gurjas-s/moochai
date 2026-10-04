package central

import (
	"log/slog"
	"testing"
)

func TestLogNeverNil(t *testing.T) {
	var nilClient *Client
	if nilClient.log() == nil {
		t.Fatal("nil client log() = nil, want slog.Default()")
	}
	bare := &Client{}
	if bare.log() == nil {
		t.Fatal("bare client log() = nil, want slog.Default()")
	}
}

func TestWithLoggerNilFallsBack(t *testing.T) {
	client := &Client{}
	client.WithLogger(nil)
	if client.log() == nil {
		t.Fatal("WithLogger(nil) log() = nil, want fallback")
	}
}

func TestWithLoggerKeepsLogger(t *testing.T) {
	want := slog.Default()
	client := &Client{}
	client.WithLogger(want)
	if client.log() != want {
		t.Fatal("WithLogger kept a different logger")
	}
}
