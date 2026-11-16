// Package store keeps the recent routes simple-magpie served, in memory, for
// the dashboard: what each request's prompt held, its cache read, the tokens
// it used, and the per-session series the Context Window page draws.
package store

import (
	"encoding/json"
	"sort"
	"sync"
	"time"

	"simple-magpie/internal/prompt"
	"simple-magpie/internal/transcript"
)

// Record is one served request.
type Record struct {
	ID       int64         `json:"id"`
	Time     time.Time     `json:"time"`
	Agent    string        `json:"agent"`
	Session  string        `json:"session"`
	Model    string        `json:"model"`
	Provider string        `json:"provider"`
	Account  string        `json:"account"`
	Protocol string        `json:"protocol"` // the API the agent spoke
	Upstream string        `json:"upstream"` // the API sent upstream
	Status   int           `json:"status"`
	Millis   int64         `json:"millis"`
	Prompt   prompt.Prompt `json:"prompt"`
	Usage    prompt.Usage  `json:"usage"`
	CacheHit float64       `json:"cacheHit"`
	Streamed bool          `json:"streamed"`
	Err      string        `json:"err,omitempty"`
	// Tried names providers/accounts attempted before this one answered.
	Tried []Try `json:"tried,omitempty"`
	// Turns is what this request (turn) added: the messages new since the
	// session's previous request, plus this request's reply, for the
	// dashboard's per-turn Conversation view.
	Turns []transcript.Turn `json:"turns,omitempty"`
	// AllCount is how many conversation turns the request carried in total (the
	// full history length), used to compute the next turn's delta.
	AllCount int `json:"allCount,omitempty"`

	// ReplyText/ReplyCalls are the answer's text and tool calls, filled by the
	// gateway while serving; they are folded into Turns before the record is
	// stored. Never serialized on their own.
	ReplyText  string                 `json:"-"`
	ReplyCalls []transcript.ReplyCall `json:"-"`
}

// Try is one attempt in the failover loop.
type Try struct {
	Provider string `json:"provider"`
	Account  string `json:"account"`
	Status   int    `json:"status"`
	Err      string `json:"err,omitempty"`
}

// Store keeps records bounded in memory.
type Store struct {
	mu    sync.RWMutex
	recs  []*Record
	seq   int64
	limit int
}

// New makes a store keeping at most limit records.
func New(limit int) *Store {
	if limit <= 0 {
		limit = 500
	}
	return &Store{limit: limit}
}

// Add appends a record and trims to the limit.
func (s *Store) Add(r *Record) *Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.seq++
	r.ID = s.seq
	if r.Time.IsZero() {
		r.Time = time.Now()
	}
	s.recs = append(s.recs, r)
	if len(s.recs) > s.limit {
		s.recs = s.recs[len(s.recs)-s.limit:]
	}
	return r
}

// Recent returns up to n records, newest first.
func (s *Store) Recent(n int) []*Record {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if n <= 0 || n > len(s.recs) {
		n = len(s.recs)
	}
	out := make([]*Record, 0, n)
	for i := len(s.recs) - 1; i >= 0 && len(out) < n; i-- {
		out = append(out, s.recs[i])
	}
	return out
}

// Get returns one record by id.
func (s *Store) Get(id int64) (*Record, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for _, r := range s.recs {
		if r.ID == id {
			return r, true
		}
	}
	return nil, false
}

// Session is what the Context Window page draws for one conversation.
type Session struct {
	Session  string     `json:"session"`
	Agent    string     `json:"agent"`
	Model    string     `json:"model"`
	Window   int        `json:"window"`
	Requests int        `json:"requests"`
	Current  *Record    `json:"current"`  // the latest request
	Series   []SeriesPt `json:"series"`   // tokens per request, oldest first
	Headroom int        `json:"headroom"` // estimated requests left
	Free     int        `json:"free"`     // tokens left in the window
}

// SeriesPt is one point of a session's token history.
type SeriesPt struct {
	ID     int64     `json:"id"`
	Time   time.Time `json:"time"`
	Tokens int       `json:"tokens"`
}

// Session builds the dashboard view for a session key, or for the most recent
// session when key is empty.
func (s *Store) Session(key string) *Session {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.recs) == 0 {
		return nil
	}
	if key == "" {
		key = s.recs[len(s.recs)-1].Session
	}
	var mine []*Record
	for _, r := range s.recs {
		if r.Session == key {
			mine = append(mine, r)
		}
	}
	if len(mine) == 0 {
		return nil
	}
	cur := mine[len(mine)-1]
	sess := &Session{
		Session:  key,
		Agent:    cur.Agent,
		Model:    cur.Model,
		Window:   cur.Prompt.Window,
		Requests: len(mine),
		Current:  cur,
	}
	var tokens []int
	for _, r := range mine {
		sess.Series = append(sess.Series, SeriesPt{ID: r.ID, Time: r.Time, Tokens: r.Prompt.Tokens})
		tokens = append(tokens, r.Prompt.Tokens)
	}
	win := cur.Prompt.Window
	if win <= 0 {
		win = 200000
		sess.Window = win
	}
	sess.Free = max(0, win-cur.Prompt.Tokens)
	sess.Headroom = prompt.Headroom(tokens, sess.Free)
	return sess
}

// Sessions lists distinct sessions, newest first.
func (s *Store) Sessions() []*Session {
	s.mu.RLock()
	seen := map[string]bool{}
	var keys []string
	for i := len(s.recs) - 1; i >= 0; i-- {
		k := s.recs[i].Session
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		keys = append(keys, k)
	}
	s.mu.RUnlock()
	var out []*Session
	for _, k := range keys {
		if sess := s.Session(k); sess != nil {
			out = append(out, sess)
		}
	}
	return out
}

// Turn is one request (轮) in a session, for the dashboard's per-turn view.
type Turn struct {
	Index  int               `json:"index"` // 1-based turn number, oldest first
	ID     int64             `json:"id"`
	Time   time.Time         `json:"time"`
	Tokens int               `json:"tokens"`
	Model  string            `json:"model"`
	Items  []transcript.Turn `json:"items"`
}

// Turns lists a session's requests as turns, oldest first, each with the
// messages it added and its token cost.
func (s *Store) Turns(key string) []Turn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.recs) == 0 {
		return nil
	}
	if key == "" {
		key = s.recs[len(s.recs)-1].Session
	}
	var out []Turn
	n := 0
	for _, r := range s.recs {
		if r.Session != key || len(r.Turns) == 0 {
			continue
		}
		n++
		items := make([]transcript.Turn, 0, len(r.Turns))
		for _, t := range r.Turns {
			if t.Kind == "tool_call" && t.Name == "" && t.Args == "" {
				continue
			}
			t.Time = r.Time
			items = append(items, t)
		}
		out = append(out, Turn{Index: n, ID: r.ID, Time: r.Time, Tokens: r.Prompt.Tokens, Model: r.Model, Items: items})
	}
	return out
}

// PriorTurnCount is how many conversation turns the session's latest request
// carried, so the next request keeps only what is new. 0 when none yet.
func (s *Store) PriorTurnCount(key string) int {
	s.mu.RLock()
	defer s.mu.RUnlock()
	for i := len(s.recs) - 1; i >= 0; i-- {
		if s.recs[i].Session == key {
			return s.recs[i].AllCount
		}
	}
	return 0
}

// Transcript builds the conversation of a session key (or the most recent one
// when key is empty) as a timeline of per-turn deltas: each request stored only
// what it added, so concatenating them in order is the whole conversation,
// stamped with each request's time for date grouping.
func (s *Store) Transcript(key string) []transcript.Turn {
	s.mu.RLock()
	defer s.mu.RUnlock()
	if len(s.recs) == 0 {
		return nil
	}
	if key == "" {
		key = s.recs[len(s.recs)-1].Session
	}
	var out []transcript.Turn
	for _, r := range s.recs {
		if r.Session != key {
			continue
		}
		for _, t := range r.Turns {
			if t.Kind == "tool_call" && t.Name == "" && t.Args == "" {
				continue
			}
			t.Time = r.Time
			out = append(out, t)
		}
	}
	return out
}

// MarshalJSON keeps nil slices as empty arrays for the UI.
func (s *Store) Stats() map[string]any {
	s.mu.RLock()
	defer s.mu.RUnlock()
	byModel := map[string]int{}
	for _, r := range s.recs {
		byModel[r.Model]++
	}
	models := make([]string, 0, len(byModel))
	for m := range byModel {
		models = append(models, m)
	}
	sort.Strings(models)
	return map[string]any{"records": len(s.recs), "models": models}
}

var _ = json.Marshal
