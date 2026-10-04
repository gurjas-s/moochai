// Package app serves a test UI for users and groups at /app.
package app

import (
	_ "embed"
	"net/http"
)

//go:embed page.html
var page string

func Register(mux *http.ServeMux) {
	mux.HandleFunc("GET /app", handlePage)
	mux.HandleFunc("GET /app/", handlePage)
}

func handlePage(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write([]byte(page))
}
