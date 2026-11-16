package protocol

import (
	"encoding/json"
)

// ---- OpenAI Chat Completions ----

type chatReq struct {
	Model       string          `json:"model"`
	Messages    []chatMessage   `json:"messages"`
	Tools       []chatTool      `json:"tools"`
	Stream      bool            `json:"stream"`
	MaxTokens   int             `json:"max_tokens"`
	Temperature *float64        `json:"temperature"`
	TopP        *float64        `json:"top_p"`
	Stop        json.RawMessage `json:"stop"`
}

type chatMessage struct {
	Role       string          `json:"role"`
	Content    json.RawMessage `json:"content"` // string or []chatPart
	ToolCalls  []chatToolCall  `json:"tool_calls,omitempty"`
	ToolCallID string          `json:"tool_call_id,omitempty"`
}

type chatPart struct {
	Type     string `json:"type"`
	Text     string `json:"text,omitempty"`
	ImageURL *struct {
		URL string `json:"url"`
	} `json:"image_url,omitempty"`
}

type chatToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type chatTool struct {
	Type     string `json:"type"`
	Function struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"function"`
}

func parseChat(body []byte) (*Request, error) {
	var in chatReq
	if err := decode(body, &in); err != nil {
		return nil, err
	}
	req := &Request{Model: in.Model, Stream: in.Stream, MaxTokens: in.MaxTokens, Temp: in.Temperature, TopP: in.TopP}
	req.Stop = parseStop(in.Stop)
	for _, m := range in.Messages {
		if m.Role == "system" || m.Role == "developer" {
			req.System = append(req.System, chatText(m.Content)...)
			continue
		}
		out := Message{Role: m.Role}
		for _, txt := range chatText(m.Content) {
			out.Parts = append(out.Parts, Part{Kind: Text, Text: txt})
		}
		for _, img := range chatImages(m.Content) {
			out.Parts = append(out.Parts, Part{Kind: Image, URL: img})
		}
		for _, tc := range m.ToolCalls {
			out.Parts = append(out.Parts, Part{Kind: ToolCall, ID: tc.ID, Name: tc.Function.Name, Args: safeArgs(tc.Function.Arguments)})
		}
		if m.Role == "tool" {
			out = Message{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: m.ToolCallID, Text: join(chatText(m.Content))}}}
		}
		req.Messages = append(req.Messages, out)
	}
	for _, t := range in.Tools {
		if t.Type != "" && t.Type != "function" {
			continue
		}
		req.Tools = append(req.Tools, Tool{Name: t.Function.Name, Description: t.Function.Description, Schema: t.Function.Parameters})
	}
	return req, nil
}

func buildChat(req *Request, model string) ([]byte, error) {
	msgs := []map[string]any{}
	if sys := join(req.System); sys != "" {
		msgs = append(msgs, map[string]any{"role": "system", "content": sys})
	}
	for _, m := range req.Messages {
		msgs = append(msgs, chatMessageOut(m)...)
	}
	out := map[string]any{"model": model, "messages": msgs}
	if req.Stream {
		out["stream"] = true
		out["stream_options"] = map[string]any{"include_usage": true}
	}
	if req.MaxTokens > 0 {
		out["max_tokens"] = req.MaxTokens
	}
	if req.Temp != nil {
		out["temperature"] = *req.Temp
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		out["stop"] = req.Stop
	}
	if len(req.Tools) > 0 {
		ts := []map[string]any{}
		for _, t := range req.Tools {
			ts = append(ts, map[string]any{"type": "function", "function": map[string]any{
				"name": t.Name, "description": t.Description, "parameters": normalizeSchema(t.Schema),
			}})
		}
		out["tools"] = ts
	}
	return json.Marshal(out)
}

func chatMessageOut(m Message) []map[string]any {
	var texts []string
	var imgs []map[string]any
	var calls []map[string]any
	var results []map[string]any
	for _, p := range m.Parts {
		switch p.Kind {
		case Text:
			texts = append(texts, p.Text)
		case Thinking:
			// Chat has no thinking field; drop on the way down (kept upstream-side when relaying)
		case Image:
			u := p.URL
			if u == "" && p.Data != "" {
				u = "data:" + p.MediaType + ";base64," + p.Data
			}
			if u != "" {
				imgs = append(imgs, map[string]any{"type": "image_url", "image_url": map[string]any{"url": u}})
			}
		case ToolCall:
			calls = append(calls, map[string]any{"id": p.ID, "type": "function", "function": map[string]any{
				"name": p.Name, "arguments": string(p.Args),
			}})
		case ToolResult:
			results = append(results, map[string]any{"role": "tool", "tool_call_id": p.CallID, "content": p.Text})
		}
	}
	var out []map[string]any
	out = append(out, results...)
	var content any
	switch {
	case len(imgs) > 0:
		parts := []map[string]any{}
		for _, tx := range texts {
			parts = append(parts, map[string]any{"type": "text", "text": tx})
		}
		parts = append(parts, imgs...)
		content = parts
	case len(texts) == 1:
		content = texts[0]
	case len(texts) > 1:
		content = join(texts)
	}
	if content != nil || len(calls) > 0 {
		msg := map[string]any{"role": m.Role}
		if content != nil {
			msg["content"] = content
		}
		if len(calls) > 0 {
			msg["tool_calls"] = calls
		}
		out = append(out, msg)
	}
	return out
}

func chatText(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil {
		if s == "" {
			return nil
		}
		return []string{s}
	}
	var parts []chatPart
	if json.Unmarshal(raw, &parts) == nil {
		var out []string
		for _, p := range parts {
			if (p.Type == "text" || p.Type == "") && p.Text != "" {
				out = append(out, p.Text)
			}
		}
		return out
	}
	return nil
}

func chatImages(raw json.RawMessage) []string {
	var parts []chatPart
	if json.Unmarshal(raw, &parts) != nil {
		return nil
	}
	var out []string
	for _, p := range parts {
		if p.Type == "image_url" && p.ImageURL != nil && p.ImageURL.URL != "" {
			out = append(out, p.ImageURL.URL)
		}
	}
	return out
}

func parseStop(raw json.RawMessage) []string {
	if len(raw) == 0 {
		return nil
	}
	var s string
	if json.Unmarshal(raw, &s) == nil && s != "" {
		return []string{s}
	}
	var ss []string
	if json.Unmarshal(raw, &ss) == nil {
		return ss
	}
	return nil
}

func safeArgs(s string) json.RawMessage {
	if s == "" {
		return json.RawMessage("{}")
	}
	if json.Valid([]byte(s)) {
		return json.RawMessage(s)
	}
	b, _ := json.Marshal(map[string]any{"_raw": s})
	return b
}

func join(s []string) string {
	switch len(s) {
	case 0:
		return ""
	case 1:
		return s[0]
	}
	return stringsJoin(s, "\n\n")
}

func normalizeSchema(s json.RawMessage) json.RawMessage {
	if len(s) == 0 || !json.Valid(s) {
		return json.RawMessage(`{"type":"object","properties":{}}`)
	}
	return s
}
