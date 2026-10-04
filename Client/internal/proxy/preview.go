package proxy

import (
	"bytes"
	"encoding/json"
	"net/http"
	"strings"
)

// previewLen is the maximum length of a prompt or answer preview in the log.
const previewLen = 120

// maxPreviewBody limits the response bytes that the proxy keeps for the answer preview.
const maxPreviewBody = 64 << 10

// preview returns the last user message of an OpenAI request body, or its prompt.
func preview(body []byte) string {
	var req struct {
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
		Prompt json.RawMessage `json:"prompt"`
	}
	if json.Unmarshal(body, &req) != nil {
		return ""
	}
	text := ""
	for i := len(req.Messages) - 1; i >= 0; i-- {
		if req.Messages[i].Role == "user" {
			text = contentText(req.Messages[i].Content)
			break
		}
	}
	if text == "" {
		_ = json.Unmarshal(req.Prompt, &text)
	}
	return clip(text)
}

// responsePreview returns the answer text of an OpenAI response body, or its error message.
// It reads plain JSON and server-sent event streams.
func responsePreview(body []byte) string {
	type answer struct {
		Choices []struct {
			Text    string `json:"text"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
			Delta struct {
				Content string `json:"content"`
			} `json:"delta"`
		} `json:"choices"`
		Error struct {
			Message string `json:"message"`
		} `json:"error"`
	}
	chunks := [][]byte{body}
	if bytes.HasPrefix(bytes.TrimSpace(body), []byte("data:")) {
		chunks = nil
		for _, line := range bytes.Split(body, []byte("\n")) {
			if data, ok := bytes.CutPrefix(bytes.TrimSpace(line), []byte("data:")); ok {
				chunks = append(chunks, data)
			}
		}
	}
	var text strings.Builder
	for _, c := range chunks {
		var a answer
		if json.Unmarshal(c, &a) != nil {
			continue
		}
		if a.Error.Message != "" {
			return clip("error: " + a.Error.Message)
		}
		for _, ch := range a.Choices {
			text.WriteString(ch.Text + ch.Message.Content + ch.Delta.Content)
		}
	}
	return clip(text.String())
}

// clip collapses white space and cuts text to previewLen characters.
func clip(text string) string {
	text = strings.Join(strings.Fields(text), " ")
	if r := []rune(text); len(r) > previewLen {
		text = string(r[:previewLen]) + "…"
	}
	return text
}

// contentText reads message content as a string or as an array of text parts.
func contentText(raw json.RawMessage) string {
	var s string
	if json.Unmarshal(raw, &s) == nil {
		return s
	}
	var parts []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	}
	_ = json.Unmarshal(raw, &parts)
	var texts []string
	for _, p := range parts {
		if p.Type == "text" {
			texts = append(texts, p.Text)
		}
	}
	return strings.Join(texts, " ")
}

// recorder keeps the response status and the start of the body for the answer preview.
// Unwrap lets ReverseProxy flush stream chunks.
type recorder struct {
	http.ResponseWriter
	status int
	body   bytes.Buffer
}

func (r *recorder) Write(b []byte) (int, error) {
	if room := maxPreviewBody - r.body.Len(); room > 0 {
		r.body.Write(b[:min(len(b), room)])
	}
	return r.ResponseWriter.Write(b)
}

func (r *recorder) WriteHeader(code int) {
	r.status = code
	r.ResponseWriter.WriteHeader(code)
}

func (r *recorder) Unwrap() http.ResponseWriter { return r.ResponseWriter }
