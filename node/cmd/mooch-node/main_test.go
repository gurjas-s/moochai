package main

import (
	"context"
	"log/slog"
	"testing"
)

func restoreDefault(t *testing.T) {
	t.Helper()
	old := slog.Default()
	t.Cleanup(func() { slog.SetDefault(old) })
}

func TestSetupLoggingRejectsBadMode(t *testing.T) {
	restoreDefault(t)
	if err := setupLogging("nope", "text"); err == nil {
		t.Fatal("setupLogging(mode=nope) error = nil, want error")
	}
}

func TestSetupLoggingRejectsBadFormat(t *testing.T) {
	restoreDefault(t)
	if err := setupLogging("requests", "yaml"); err == nil {
		t.Fatal("setupLogging(format=yaml) error = nil, want error")
	}
}

func TestSetupLoggingLevels(t *testing.T) {
	restoreDefault(t)
	if err := setupLogging("requests", "text"); err != nil {
		t.Fatalf("setupLogging(requests): %v", err)
	}
	if slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		t.Error("requests mode enables Debug, want Debug off")
	}
	if err := setupLogging("dev", "json"); err != nil {
		t.Fatalf("setupLogging(dev): %v", err)
	}
	if !slog.Default().Enabled(context.Background(), slog.LevelDebug) {
		t.Error("dev mode disables Debug, want Debug on")
	}
}

func TestLogDefaultReadsEnv(t *testing.T) {
	t.Setenv("MOOCH_LOG_MODE_TEST", "dev")
	if got := logDefault("MOOCH_LOG_MODE_TEST", "requests"); got != "dev" {
		t.Errorf("logDefault = %q, want dev", got)
	}
	if got := logDefault("MOOCH_LOG_MODE_TEST_EMPTY", "requests"); got != "requests" {
		t.Errorf("logDefault fallback = %q, want requests", got)
	}
}
