// Package respond writes JSON responses and OpenAI-shaped errors.
package respond

import (
	"encoding/json"
	"net/http"
)

var errorTypes = map[int]string{
	http.StatusNotFound:           "model_not_found",
	http.StatusBadGateway:         "service_unavailable",
	http.StatusServiceUnavailable: "service_unavailable",
}

type apiError struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    int    `json:"code"`
}

func JSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func Error(w http.ResponseWriter, status int, msg string) {
	errType, ok := errorTypes[status]
	if !ok {
		errType = "invalid_request_error"
	}
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]apiError{"error": {msg, errType, status}})
}
