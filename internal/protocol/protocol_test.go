package protocol

import (
	"encoding/json"
	"testing"
)

func TestChatRoundtripAnthropic(t *testing.T) {
	chat, _ := json.Marshal(map[string]any{
		"model": "m",
		"messages": []map[string]any{
			{"role": "system", "content": "be brief"},
			{"role": "user", "content": "hello"},
		},
	})
	req, err := Parse(Chat, chat)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.System) != 1 || req.System[0] != "be brief" {
		t.Fatalf("system = %v", req.System)
	}
	out, err := Build(Anthropic, req, "claude-x")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Model    string `json:"model"`
		System   string `json:"system"`
		Messages []struct {
			Role    string          `json:"role"`
			Content json.RawMessage `json:"content"`
		} `json:"messages"`
	}
	if err := json.Unmarshal(out, &v); err != nil {
		t.Fatal(err)
	}
	if v.Model != "claude-x" || v.System != "be brief" {
		t.Fatalf("built anth wrong: %+v", v)
	}
	if len(v.Messages) != 1 || v.Messages[0].Role != "user" {
		t.Fatalf("messages = %+v", v.Messages)
	}
}

func TestAnthropicToolsRoundtrip(t *testing.T) {
	anth, _ := json.Marshal(map[string]any{
		"model":      "m",
		"max_tokens": 100,
		"tools": []map[string]any{
			{"name": "bash", "description": "run", "input_schema": map[string]any{"type": "object"}},
		},
		"messages": []map[string]any{
			{"role": "user", "content": []map[string]any{{"type": "text", "text": "go"}}},
		},
	})
	req, err := Parse(Anthropic, anth)
	if err != nil {
		t.Fatal(err)
	}
	if len(req.Tools) != 1 || req.Tools[0].Name != "bash" {
		t.Fatalf("tools = %+v", req.Tools)
	}
	out, err := Build(Chat, req, "gpt")
	if err != nil {
		t.Fatal(err)
	}
	var v struct {
		Tools []struct {
			Function struct {
				Name string `json:"name"`
			} `json:"function"`
		} `json:"tools"`
	}
	json.Unmarshal(out, &v)
	if len(v.Tools) != 1 || v.Tools[0].Function.Name != "bash" {
		t.Fatalf("chat tools wrong: %+v", v)
	}
}

func TestDetect(t *testing.T) {
	cases := map[string]Protocol{
		"/v1/chat/completions": Chat,
		"/v1/responses":        Responses,
		"/v1/messages":         Anthropic,
		"/v1beta/models/gemini-pro:generateContent": Gemini,
	}
	for path, want := range cases {
		if got := Detect(path); got != want {
			t.Errorf("Detect(%q) = %q, want %q", path, got, want)
		}
	}
}
