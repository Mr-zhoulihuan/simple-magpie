// Package web serves simple-magpie's Context Window dashboard and its JSON
// API. The page is embedded and self-contained: it draws the treemap legend,
// the cache and the headroom numbers from the store's records.
package web

import (
	_ "embed"
	"encoding/json"
	"net/http"
	"strconv"

	"simple-magpie/internal/prompt"
	"simple-magpie/internal/store"
	"simple-magpie/internal/transcript"
)

//go:embed dashboard.html
var dashboardHTML []byte

// Server serves the dashboard.
type Server struct {
	store *store.Store
}

// New makes a dashboard server.
func New(st *store.Store) *Server { return &Server{store: st} }

// Handler returns the dashboard routes.
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /dashboard", s.page)
	mux.HandleFunc("GET /dashboard/session", s.session)
	mux.HandleFunc("GET /dashboard/transcript", s.transcript)
	mux.HandleFunc("GET /dashboard/turns", s.turns)
	mux.HandleFunc("GET /dashboard/sessions", s.sessions)
	mux.HandleFunc("GET /dashboard/recent", s.recent)
	mux.HandleFunc("GET /dashboard/record", s.record)
	return mux
}

func (s *Server) page(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write(dashboardHTML)
}

func (s *Server) session(w http.ResponseWriter, r *http.Request) {
	sess := s.store.Session(r.URL.Query().Get("session"))
	if sess == nil {
		writeJSON(w, 200, map[string]any{"empty": true})
		return
	}
	view := map[string]any{
		"session":  sess.Session,
		"agent":    sess.Agent,
		"model":    sess.Model,
		"window":   sess.Window,
		"requests": sess.Requests,
		"headroom": sess.Headroom,
		"free":     sess.Free,
		"series":   sess.Series,
		"prompt":   sess.Current.Prompt,
		"usage":    sess.Current.Usage,
		"cacheHit": sess.Current.CacheHit,
		"turns":    sess.Current.Prompt.Turns,
	}
	writeJSON(w, 200, view)
}

// turns serves a session's requests as per-turn items (messages + tool calls),
// each with its token cost, for the dashboard's 会话内容 view.
func (s *Server) turns(w http.ResponseWriter, r *http.Request) {
	list := s.store.Turns(r.URL.Query().Get("session"))
	if list == nil {
		list = []store.Turn{}
	}
	writeJSON(w, 200, map[string]any{"turns": list})
}

// transcript serves the conversation of a session: messages, tool calls and
// their results.
func (s *Server) transcript(w http.ResponseWriter, r *http.Request) {
	turns := s.store.Transcript(r.URL.Query().Get("session"))
	if turns == nil {
		turns = []transcript.Turn{}
	}
	writeJSON(w, 200, map[string]any{"turns": turns})
}

func (s *Server) sessions(w http.ResponseWriter, r *http.Request) {
	list := s.store.Sessions()
	out := make([]map[string]any, 0, len(list))
	for _, sess := range list {
		out = append(out, map[string]any{
			"session": sess.Session, "agent": sess.Agent, "model": sess.Model,
			"requests": sess.Requests, "window": sess.Window, "tokens": sess.Current.Prompt.Tokens,
		})
	}
	writeJSON(w, 200, map[string]any{"sessions": out})
}

func (s *Server) recent(w http.ResponseWriter, r *http.Request) {
	n := 30
	if v := r.URL.Query().Get("n"); v != "" {
		if i, err := strconv.Atoi(v); err == nil {
			n = i
		}
	}
	recs := s.store.Recent(n)
	out := make([]map[string]any, 0, len(recs))
	for _, rec := range recs {
		out = append(out, map[string]any{
			"id": rec.ID, "time": rec.Time, "agent": rec.Agent, "session": rec.Session,
			"model": rec.Model, "provider": rec.Provider, "status": rec.Status,
			"tokens": rec.Prompt.Tokens, "cacheHit": rec.CacheHit, "tried": rec.Tried,
		})
	}
	writeJSON(w, 200, map[string]any{"records": out})
}

func (s *Server) record(w http.ResponseWriter, r *http.Request) {
	id, _ := strconv.ParseInt(r.URL.Query().Get("id"), 10, 64)
	rec, ok := s.store.Get(id)
	if !ok {
		http.NotFound(w, r)
		return
	}
	writeJSON(w, 200, rec)
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

var _ = prompt.Order
