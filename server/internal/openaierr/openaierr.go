// Package openaierr writes OpenAI-shaped JSON error envelopes.
package openaierr

import (
	"encoding/json"
	"net/http"
)

// Error types that central sends.
const (
	TypeInvalidRequest = "invalid_request_error"
	TypeNotFound       = "model_not_found"
	TypeUnavailable    = "service_unavailable"
	TypeAuth           = "authentication_error"
	TypeForbidden      = "permission_denied"
)

// Write sends an error envelope with status.
func Write(w http.ResponseWriter, status int, errType, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	var e struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
			Code    int    `json:"code"`
		} `json:"error"`
	}
	e.Error.Message, e.Error.Type, e.Error.Code = msg, errType, status
	_ = json.NewEncoder(w).Encode(e)
}
