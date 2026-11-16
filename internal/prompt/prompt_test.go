package prompt

import (
	"encoding/json"
	"testing"

	"simple-magpie/internal/protocol"
)

func TestAnalyzeChat(t *testing.T) {
	body, _ := json.Marshal(map[string]any{
		"model": "deepseek-chat",
		"messages": []map[string]any{
			{"role": "system", "content": "You are a coding agent. Read AGENTS.md first."},
			{"role": "user", "content": "please fix the bug"},
			{"role": "assistant", "content": "", "tool_calls": []map[string]any{
				{"id": "c1", "type": "function", "function": map[string]any{"name": "bash", "arguments": `{"cmd":"ls"}`}},
			}},
			{"role": "tool", "tool_call_id": "c1", "content": "gateway.go host.go lots of output here"},
		},
		"tools": []map[string]any{
			{"type": "function", "function": map[string]any{"name": "bash", "description": "run a shell", "parameters": map[string]any{"type": "object"}}},
		},
	})
	p := Analyze(protocol.Chat, body)
	if p.Tokens <= 0 {
		t.Fatalf("tokens = %d, want > 0", p.Tokens)
	}
	kinds := map[string]bool{}
	for _, part := range p.Parts {
		kinds[part.Kind] = true
	}
	if !kinds[PartTools] {
		t.Errorf("missing tools part")
	}
	if !kinds[PartChat] {
		t.Errorf("missing chat part")
	}
	if !kinds[PartResults] {
		t.Errorf("missing results part (bash output)")
	}
	if !kinds[PartMemory] {
		t.Errorf("missing memory part (AGENTS.md mentioned)")
	}
}

func TestHeadroom(t *testing.T) {
	// grows ~1000 tokens a turn, 10000 free -> ~10 requests
	series := []int{10000, 11000, 12000, 13000, 14000}
	if got := Headroom(series, 10000); got != 10 {
		t.Fatalf("Headroom = %d, want 10", got)
	}
	// no growth
	if got := Headroom([]int{5000, 5000, 5000}, 10000); got != 0 {
		t.Fatalf("Headroom flat = %d, want 0", got)
	}
	// too few points
	if got := Headroom([]int{5000}, 10000); got != 0 {
		t.Fatalf("Headroom single = %d, want 0", got)
	}
}

func TestCacheHit(t *testing.T) {
	u := Usage{Input: 1000, CacheRead: 9000}
	if h := u.CacheHit(); h < 0.89 || h > 0.91 {
		t.Fatalf("CacheHit = %v, want ~0.9", h)
	}
}

func TestCalibrate(t *testing.T) {
	p := Prompt{Tokens: 100, Parts: []PromptPart{{Kind: PartChat, Tokens: 100}}}
	p.Calibrate(200)
	if p.Tokens != 200 || !p.Counted {
		t.Fatalf("calibrate = %+v", p)
	}
	if p.Parts[0].Tokens != 200 {
		t.Fatalf("part not scaled: %d", p.Parts[0].Tokens)
	}
}

func TestTokensOf(t *testing.T) {
	// ASCII: 4 chars ~ 1 token
	if n := TokensOf("abcdefgh"); n != 2 {
		t.Fatalf("TokensOf ascii = %d, want 2", n)
	}
	// CJK: 1 char ~ 1 token
	if n := TokensOf("你好世界"); n != 4 {
		t.Fatalf("TokensOf cjk = %d, want 4", n)
	}
}
