package server

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"peer-ai-client/internal/config"
)

func TestProbeServiceMarksHealthy(t *testing.T) {
	var gotPath string
	backend := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		w.WriteHeader(http.StatusOK)
	}))
	defer backend.Close()

	service := config.Service{ID: "llm", Endpoint: backend.URL, APIBase: "/v1"}
	if err := ProbeService(context.Background(), &service, time.Second); err != nil {
		t.Fatalf("ProbeService() error = %v", err)
	}
	if !service.Healthy {
		t.Fatal("Healthy = false, want true")
	}
	if gotPath != "/v1/models" {
		t.Fatalf("probe path = %q, want /v1/models", gotPath)
	}
}

func TestProbeServiceMarksUnhealthyOnBackendError(t *testing.T) {
	service := config.Service{
		ID: "llm", Endpoint: "http://127.0.0.1:1", APIBase: "/v1", Healthy: true,
	}
	if err := ProbeService(context.Background(), &service, 50*time.Millisecond); err == nil {
		t.Fatal("ProbeService() error = nil, want error")
	}
	if service.Healthy {
		t.Fatal("Healthy = true, want false")
	}
}

func TestProbeServiceMarksUnhealthyOnHTTPError(t *testing.T) {
	backend := httptest.NewServer(http.NotFoundHandler())
	defer backend.Close()
	service := config.Service{ID: "llm", Endpoint: backend.URL, APIBase: "/v1", Healthy: true}
	if err := ProbeService(context.Background(), &service, time.Second); err == nil {
		t.Fatal("ProbeService() error = nil, want error")
	}
	if service.Healthy {
		t.Fatal("Healthy = true, want false")
	}
}

func TestProbeServiceRejectsInvalidInput(t *testing.T) {
	if err := ProbeService(context.Background(), nil, time.Second); err == nil {
		t.Fatal("nil service error = nil, want error")
	}
	service := config.Service{ID: "llm", Endpoint: "http://127.0.0.1", APIBase: "/v1", Healthy: true}
	if err := ProbeService(context.Background(), &service, 0); err == nil {
		t.Fatal("zero timeout error = nil, want error")
	}
	if service.Healthy {
		t.Fatal("Healthy = true, want false")
	}
}
