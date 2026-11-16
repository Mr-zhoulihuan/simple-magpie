package store

import (
	"testing"
	"time"

	"simple-magpie/internal/prompt"
)

func TestStoreLimitAndSession(t *testing.T) {
	s := New(3)
	for i := 0; i < 5; i++ {
		s.Add(&Record{
			Session: "s1", Agent: "a", Model: "m",
			Time:   time.Now(),
			Prompt: prompt.Prompt{Tokens: 100 + i*50, Window: 1000},
		})
	}
	recent := s.Recent(10)
	if len(recent) != 3 {
		t.Fatalf("limit not enforced: %d", len(recent))
	}
	sess := s.Session("s1")
	if sess == nil {
		t.Fatal("no session")
	}
	if sess.Requests != 3 {
		t.Fatalf("requests = %d, want 3 (limited window)", sess.Requests)
	}
	// grows 50 a turn, free = 1000 - latest
	if sess.Headroom <= 0 {
		t.Fatalf("headroom = %d, want > 0", sess.Headroom)
	}
}

func TestRecentNewestFirst(t *testing.T) {
	s := New(10)
	s.Add(&Record{Session: "a", Prompt: prompt.Prompt{Tokens: 1}})
	last := s.Add(&Record{Session: "b", Prompt: prompt.Prompt{Tokens: 2}})
	r := s.Recent(1)
	if len(r) != 1 || r[0].ID != last.ID {
		t.Fatalf("recent[0] = %+v, want id %d", r, last.ID)
	}
}
