// Package gateway is simple-magpie's local LLM gateway: it serves the wire
// APIs agents speak (OpenAI Chat, OpenAI Responses, Anthropic Messages,
// Gemini), routes each request to a provider/account, relays it as-is when the
// provider speaks the same API or translates between APIs via the IR, and
// records what each prompt held for the dashboard.
package gateway

import (
	"bytes"
	"encoding/json"
	"io"
	"log"
	"net/http"
	"strings"
	"time"

	"simple-magpie/internal/config"
	"simple-magpie/internal/prompt"
	"simple-magpie/internal/protocol"
	"simple-magpie/internal/routing"
	"simple-magpie/internal/store"
	"simple-magpie/internal/transcript"
)

// Token is the bearer token agents are told to use. The gateway listens on
// loopback and accepts anything, but agents insist on one.
const Token = "smagpie"

// Server is the gateway.
type Server struct {
	cfg    *config.Config
	router *routing.Router
	store  *store.Store
	client *http.Client
}

// New makes a server.
func New(cfg *config.Config, st *store.Store) *Server {
	return &Server{
		cfg:    cfg,
		router: routing.New(cfg),
		store:  st,
		client: &http.Client{Timeout: 0}, // streams may be long; no overall deadline
	}
}

// Handler returns the HTTP mux.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/models", s.models)
	mux.HandleFunc("POST /v1/chat/completions", s.handle)
	mux.HandleFunc("POST /chat/completions", s.handle)
	mux.HandleFunc("POST /v1/responses", s.handle)
	mux.HandleFunc("POST /responses", s.handle)
	mux.HandleFunc("POST /v1/messages", s.handle)
	mux.HandleFunc("POST /messages", s.handle)
	mux.HandleFunc("POST /v1beta/models/{call...}", s.handle)
	mux.HandleFunc("GET /", s.index)
	return mux
}

func (s *Server) index(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, 200, map[string]any{"name": "simple-magpie", "token": Token,
		"endpoints": []string{"/v1/chat/completions", "/v1/responses", "/v1/messages", "/v1beta/models/{model}:generateContent", "/dashboard"}})
}

func (s *Server) models(w http.ResponseWriter, r *http.Request) {
	var data []map[string]any
	for _, id := range s.cfg.ProviderIDs() {
		p, _ := s.cfg.Provider(id)
		if p.DefaultModel != "" {
			data = append(data, map[string]any{"id": id + "/" + p.DefaultModel, "object": "model", "owned_by": id})
		}
	}
	for i := range s.cfg.Groups {
		g := &s.cfg.Groups[i]
		data = append(data, map[string]any{"id": "group/" + g.ID, "object": "model", "owned_by": "group"})
	}
	writeJSON(w, 200, map[string]any{"object": "list", "data": data})
}

// handle serves one request: parse model, plan candidates, try each.
func (s *Server) handle(w http.ResponseWriter, r *http.Request) {
	start := time.Now()
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeError(w, protocol.Chat, 400, err.Error())
		return
	}
	from := protocol.Detect(r.URL.Path)
	// Gemini keeps the model in the path; move it into the body so Plan sees it.
	if from == protocol.Gemini {
		body = geminiModelFromPath(r.URL.Path, body)
	}
	model := modelOf(body)
	session := sessionOf(r)
	rec := &store.Record{
		Agent: agentOf(r), Session: session, Model: model,
		Protocol: string(from), Streamed: streamOf(body),
	}
	// analyze the prompt once, before translation.
	rec.Prompt = prompt.Analyze(from, body)
	rec.Prompt = analyzeWithWindow(rec.Prompt, model, s)
	// the conversation the request carried. Only the messages new since the
	// session's last request are kept as this turn's content, so the dashboard
	// shows each turn once, not the whole history every time.
	if req, err := protocol.Parse(from, body); err == nil {
		full := transcript.FromRequest(req)
		rec.AllCount = len(full)
		start := 0
		if prior := s.store.PriorTurnCount(session); prior > 0 && prior <= len(full) {
			start = prior
		}
		rec.Turns = full[start:]
	}

	cands, resolved := s.router.Plan(model, session)
	if len(cands) == 0 {
		rec.Status = 404
		rec.Err = "no provider serves " + model
		rec.Millis = time.Since(start).Milliseconds()
		s.store.Add(rec)
		writeError(w, from, 404, "no provider serves model "+strconvQuote(model))
		return
	}
	rec.Model = resolved

	// try candidates in order; move on for a retryable failure before any body
	// has been sent to the client.
	for i, c := range cands {
		p, ok := s.cfg.Provider(c.Provider)
		if !ok {
			continue
		}
		status, served, retryable := s.tryOne(w, r, from, p, c, body, rec)
		if served {
			rec.Provider, rec.Account, rec.Status = c.Provider, c.Account.Name, status
			rec.Millis = time.Since(start).Milliseconds()
			// append the assistant's answer for this request to the transcript
			// only real tool calls (a streamed fragment with no name is not one)
			calls := rec.ReplyCalls[:0]
			for _, c := range rec.ReplyCalls {
				if c.Name != "" {
					calls = append(calls, c)
				}
			}
			rec.ReplyCalls = calls
			if rec.ReplyText != "" || len(rec.ReplyCalls) > 0 {
				rec.Turns = transcript.AppendReply(rec.Turns, transcript.ReplyTurn{Text: rec.ReplyText, Calls: rec.ReplyCalls})
			}
			// calibrate the prompt's estimate to the vendor's real total so the
			// dashboard's "used" and part sizes are the counted ones.
			if rec.Usage.Known && rec.Usage.Input+rec.Usage.CacheRead+rec.Usage.CacheWrite > 0 {
				rec.Prompt.Calibrate(rec.Usage.Input + rec.Usage.CacheRead + rec.Usage.CacheWrite)
			}
			rec.CacheHit = rec.Usage.CacheHit()
			s.router.Served(session, c.Provider, c.Account.Name)
			s.store.Add(rec)
			return
		}
		rec.Tried = append(rec.Tried, store.Try{Provider: c.Provider, Account: c.Account.Name, Status: status, Err: rec.Err})
		if !retryable {
			break
		}
		if i < len(cands)-1 {
			// brief rest so the next request avoids this account
			s.router.Rest(c.Provider, c.Account.Name, 30*time.Second)
		}
	}
	// every candidate failed: the last error already written, or write one.
	if rec.Status == 0 {
		rec.Status = 502
	}
	rec.Millis = time.Since(start).Milliseconds()
	s.store.Add(rec)
}

// tryOne makes one attempt. served is true when the reply was written to w.
// retryable is true when the failure is one another candidate may handle.
func (s *Server) tryOne(w http.ResponseWriter, r *http.Request, from protocol.Protocol, p *config.Provider, c routing.Candidate, body []byte, rec *store.Record) (status int, served, retryable bool) {
	// choose the upstream API: the same one if the provider speaks it, else the
	// first the provider speaks.
	to := from
	if !p.Speaks(string(to)) {
		to = firstProtocol(p)
	}
	rec.Upstream = string(to)

	var sendBody []byte
	relay := p.Speaks(string(from))
	if relay {
		sendBody = body
	} else {
		req, err := protocol.Parse(from, body)
		if err != nil {
			rec.Status = 400
			rec.Err = err.Error()
			writeError(w, from, 400, err.Error())
			return 400, false, false
		}
		sendBody, err = protocol.Build(to, req, c.Model)
		if err != nil {
			rec.Status = 500
			rec.Err = err.Error()
			writeError(w, from, 500, err.Error())
			return 500, false, false
		}
	}
	// the model field: keep the provider's own model name (c.Model)
	sendBody = rewriteModel(sendBody, c.Model)

	url := protocol.UpstreamPath(p.BaseURL, to, c.Model, streamOf(sendBody))
	req, err := http.NewRequestWithContext(r.Context(), http.MethodPost, url, bytes.NewReader(sendBody))
	if err != nil {
		rec.Status = 502
		rec.Err = err.Error()
		writeError(w, from, 502, err.Error())
		return 502, false, true
	}
	copyHeaders(req.Header, r.Header, to, c.Account.Key)
	if s := r.Header.Get("session_id"); s != "" {
		req.Header.Set("session_id", s)
	}

	res, err := s.client.Do(req)
	if err != nil {
		rec.Status = 502
		rec.Err = err.Error()
		writeError(w, from, 502, p.Name+": "+err.Error())
		return 502, false, true
	}
	defer res.Body.Close()

	if res.StatusCode >= 400 {
		b, _ := io.ReadAll(io.LimitReader(res.Body, 1<<20))
		rec.Status = res.StatusCode
		rec.Err = string(b)
		retry := res.StatusCode == 429 || res.StatusCode == 402 || res.StatusCode >= 500
		if !retry {
			// a client error: pass it through as the answer
			copyPassHeaders(w.Header(), res.Header)
			w.WriteHeader(res.StatusCode)
			w.Write(b)
		}
		return res.StatusCode, !retry, retry
	}

	// relay a same-API reply as-is; translate a different-API reply.
	if relay {
		s.relay(res, w, from, rec)
	} else {
		s.translateReply(res, w, from, to, rec)
	}
	return res.StatusCode, true, false
}

// firstProtocol is the first API a provider speaks.
func firstProtocol(p *config.Provider) protocol.Protocol {
	if len(p.Protocols) > 0 {
		return protocol.Protocol(p.Protocols[0])
	}
	return protocol.Chat
}

func copyHeaders(dst, src http.Header, to protocol.Protocol, key string) {
	for k, vs := range src {
		lk := strings.ToLower(k)
		if isHop(lk) || lk == "host" || lk == "content-length" || lk == "authorization" || lk == "x-api-key" {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
	switch to {
	case protocol.Anthropic:
		dst.Set("x-api-key", key)
		dst.Set("anthropic-version", orDefault(src.Get("anthropic-version"), "2023-06-01"))
	case protocol.Gemini:
		dst.Set("x-goog-api-key", key)
	default:
		dst.Set("Authorization", "Bearer "+key)
	}
}

func copyPassHeaders(dst, src http.Header) {
	for k, vs := range src {
		if isHop(strings.ToLower(k)) {
			continue
		}
		for _, v := range vs {
			dst.Add(k, v)
		}
	}
}

func isHop(k string) bool {
	switch k {
	case "connection", "keep-alive", "proxy-authenticate", "proxy-authorization",
		"te", "trailer", "transfer-encoding", "upgrade", "content-encoding":
		return true
	}
	return false
}

func orDefault(a, b string) string {
	if a != "" {
		return a
	}
	return b
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func writeError(w http.ResponseWriter, proto protocol.Protocol, status int, msg string) {
	switch proto {
	case protocol.Anthropic:
		writeJSON(w, status, map[string]any{"type": "error", "error": map[string]any{"type": "api_error", "message": msg}})
	default:
		writeJSON(w, status, map[string]any{"error": map[string]any{"message": msg, "type": "api_error"}})
	}
}

func strconvQuote(s string) string { return "\"" + s + "\"" }

// logf logs a line a vendor said, for debugging.
func logf(format string, a ...any) { log.Printf(format, a...) }
