package app

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestPage(t *testing.T) {
	mux := http.NewServeMux()
	Register(mux)
	for _, path := range []string{"/app", "/app/"} {
		rec := httptest.NewRecorder()
		mux.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, path, nil))
		if rec.Code != 200 {
			t.Fatalf("%s: status %d", path, rec.Code)
		}
		body := rec.Body.String()
		for _, want := range []string{"Mooch.ai Test UI", "/api/nodes", "/v1/models"} {
			if !strings.Contains(body, want) {
				t.Fatalf("%s: page does not contain %q", path, want)
			}
		}
		if ct := rec.Header().Get("Content-Type"); !strings.Contains(ct, "text/html") {
			t.Fatalf("content type = %q", ct)
		}
	}
}
