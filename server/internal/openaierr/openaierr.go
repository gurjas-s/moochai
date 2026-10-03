// Package openaierr writes OpenAI-shaped JSON error envelopes.
//
// OpenAI clients (OpenCode, SDKs) parse this shape. Central uses it for
// each error that central makes itself.
package openaierr

import (
	"encoding/json"
	"net/http"
)

// Error types that central sends.
const (
	TypeInvalidRequest = "invalid_request_error"
	TypeModelNotFound  = "model_not_found"
	TypeBadGateway     = "bad_gateway"
	TypeTooLarge       = "request_too_large"
)

type envelope struct {
	Error body `json:"error"`
}

type body struct {
	Message string `json:"message"`
	Type    string `json:"type"`
	Code    int    `json:"code"`
}

// Write sends status and an OpenAI error envelope with errType and msg.
func Write(w http.ResponseWriter, status int, errType, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(envelope{Error: body{Message: msg, Type: errType, Code: status}})
}
