// Package app serves a minimal test UI for users and groups.
// The page calls the JSON API, so operators can test private
// sharing from a browser on the tailnet.
package app

import (
	_ "embed"
	"log/slog"
	"net/http"
)

// page is the test UI. It lives in page.html for easy edits.
// The binary embeds it, so central serves it with no extra files.
//
//go:embed page.html
var page string

// Handler serves the test UI.
type Handler struct {
	log *slog.Logger
}

// New returns a Handler. A nil log means slog.Default.
func New(log *slog.Logger) *Handler {
	if log == nil {
		log = slog.Default()
	}
	return &Handler{log: log.With("component", "app")}
}

// Register adds the routes to mux.
func (h *Handler) Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /app", h.handlePage)
	mux.HandleFunc("GET /app/", h.handlePage)
}

func (h *Handler) handlePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(page))
}
