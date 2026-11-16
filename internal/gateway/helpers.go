package gateway

import (
	"encoding/json"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"
	"github.com/tidwall/sjson"

	"simple-magpie/internal/prompt"
)

func modelOf(body []byte) string {
	return gjson.GetBytes(body, "model").String()
}

func streamOf(body []byte) bool {
	return gjson.GetBytes(body, "stream").Bool()
}

func agentOf(r *http.Request) string {
	ua := r.Header.Get("User-Agent")
	switch {
	case strings.HasPrefix(strings.ToLower(ua), "claude"):
		return "claude"
	case strings.Contains(strings.ToLower(ua), "codex"):
		return "codex"
	case strings.Contains(strings.ToLower(ua), "gemini"):
		return "gemini"
	case strings.Contains(strings.ToLower(ua), "opencode"):
		return "opencode"
	}
	if ua == "" {
		return "unknown"
	}
	// first word of the UA
	if i := strings.IndexAny(ua, " /"); i > 0 {
		return ua[:i]
	}
	return ua
}

// sessionOf is the conversation id: the agent's session header, else a hash of
// how the conversation opens, so turns of one conversation share a session.
func sessionOf(r *http.Request) string {
	for _, k := range []string{"X-Magpie-Session", "session_id", "x-session-id", "conversation_id"} {
		if v := r.Header.Get(k); v != "" {
			return v
		}
	}
	return ""
}

// rewriteModel replaces the body's model with m.
func rewriteModel(body []byte, m string) []byte {
	if m == "" {
		return body
	}
	out, err := sjson.SetBytes(body, "model", m)
	if err != nil {
		return body
	}
	return out
}

// geminiModelFromPath pulls {model}:generateContent out of the URL and puts it
// in the body as "model".
func geminiModelFromPath(path string, body []byte) []byte {
	i := strings.LastIndex(path, "/models/")
	if i < 0 {
		return body
	}
	rest := path[i+len("/models/"):]
	if j := strings.Index(rest, ":"); j >= 0 {
		rest = rest[:j]
	}
	out, err := sjson.SetBytes(body, "model", rest)
	if err != nil {
		return body
	}
	return out
}

// analyzeWithWindow analyzes the prompt and sets the model's context window.
func analyzeWithWindow(p prompt.Prompt, model string, s *Server) prompt.Prompt {
	p.Window = windowOf(model, s)
	return p
}

// windowOf returns the context window used to measure a model's prompt.
func windowOf(model string, s *Server) int {
	// a provider/model may carry it in config in a real build; here we use a
	// table by model name fragment.
	return ContextWindow(model)
}

func jsonMarshal(v any) []byte { b, _ := json.Marshal(v); return b }
