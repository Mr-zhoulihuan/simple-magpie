package protocol

import "encoding/json"

// ---- Anthropic Messages ----

type anthropicReq struct {
	Model       string             `json:"model"`
	System      json.RawMessage    `json:"system"`
	Messages    []anthropicMessage `json:"messages"`
	Tools       []anthropicTool    `json:"tools"`
	Stream      bool               `json:"stream"`
	MaxTokens   int                `json:"max_tokens"`
	Temperature *float64           `json:"temperature"`
	TopP        *float64           `json:"top_p"`
	Stop        []string           `json:"stop_sequences"`
}

type anthropicMessage struct {
	Role    string          `json:"role"`
	Content json.RawMessage `json:"content"` // string or []anthropicBlock
}

type anthropicBlock struct {
	Type string `json:"type"`

	// text / thinking
	Text      string `json:"text,omitempty"`
	Thinking  string `json:"thinking,omitempty"`
	Signature string `json:"signature,omitempty"`

	// tool_use
	ID    string          `json:"id,omitempty"`
	Name  string          `json:"name,omitempty"`
	Input json.RawMessage `json:"input,omitempty"`

	// tool_result
	ToolUseID string          `json:"tool_use_id,omitempty"`
	Content   json.RawMessage `json:"content,omitempty"`
	IsError   bool            `json:"is_error,omitempty"`

	// image
	Source *struct {
		Type      string `json:"type"`
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
		URL       string `json:"url"`
	} `json:"source,omitempty"`
}

type anthropicTool struct {
	Name        string          `json:"name"`
	Description string          `json:"description"`
	InputSchema json.RawMessage `json:"input_schema"`
}

func parseAnthropic(body []byte) (*Request, error) {
	var in anthropicReq
	if err := decode(body, &in); err != nil {
		return nil, err
	}
	req := &Request{Model: in.Model, Stream: in.Stream, MaxTokens: in.MaxTokens, Temp: in.Temperature, TopP: in.TopP, Stop: in.Stop}
	if len(in.System) > 0 {
		var s string
		if json.Unmarshal(in.System, &s) == nil {
			if s != "" {
				req.System = append(req.System, s)
			}
		} else {
			var blocks []anthropicBlock
			if json.Unmarshal(in.System, &blocks) == nil {
				for _, b := range blocks {
					if b.Text != "" {
						req.System = append(req.System, b.Text)
					}
				}
			}
		}
	}
	for _, m := range in.Messages {
		out := Message{Role: m.Role}
		var blocks []anthropicBlock
		if json.Unmarshal(m.Content, &blocks) != nil {
			var s string
			if json.Unmarshal(m.Content, &s) == nil && s != "" {
				out.Parts = append(out.Parts, Part{Kind: Text, Text: s})
			}
			req.Messages = append(req.Messages, out)
			continue
		}
		for _, b := range blocks {
			switch b.Type {
			case "text":
				out.Parts = append(out.Parts, Part{Kind: Text, Text: b.Text})
			case "thinking":
				out.Parts = append(out.Parts, Part{Kind: Thinking, Text: b.Thinking, Signature: b.Signature})
			case "tool_use":
				out.Parts = append(out.Parts, Part{Kind: ToolCall, ID: b.ID, Name: b.Name, Args: b.Input})
			case "tool_result":
				out.Parts = append(out.Parts, Part{Kind: ToolResult, CallID: b.ToolUseID, Text: join(chatText(b.Content)), IsError: b.IsError})
			case "image":
				if b.Source != nil {
					out.Parts = append(out.Parts, Part{Kind: Image, MediaType: b.Source.MediaType, Data: b.Source.Data, URL: b.Source.URL})
				}
			}
		}
		req.Messages = append(req.Messages, out)
	}
	for _, t := range in.Tools {
		req.Tools = append(req.Tools, Tool{Name: t.Name, Description: t.Description, Schema: t.InputSchema})
	}
	return req, nil
}

func buildAnthropic(req *Request, model string) ([]byte, error) {
	msgs := []map[string]any{}
	for _, m := range req.Messages {
		msgs = append(msgs, map[string]any{"role": m.Role, "content": anthropicBlocks(m)})
	}
	out := map[string]any{"model": model, "messages": msgs, "max_tokens": orInt(req.MaxTokens, 4096)}
	if sys := join(req.System); sys != "" {
		out["system"] = sys
	}
	if req.Stream {
		out["stream"] = true
	}
	if req.Temp != nil {
		out["temperature"] = *req.Temp
	}
	if req.TopP != nil {
		out["top_p"] = *req.TopP
	}
	if len(req.Stop) > 0 {
		out["stop_sequences"] = req.Stop
	}
	if len(req.Tools) > 0 {
		ts := []map[string]any{}
		for _, t := range req.Tools {
			ts = append(ts, map[string]any{"name": t.Name, "description": t.Description, "input_schema": normalizeSchema(t.Schema)})
		}
		out["tools"] = ts
	}
	return json.Marshal(out)
}

func anthropicBlocks(m Message) []map[string]any {
	var out []map[string]any
	for _, p := range m.Parts {
		switch p.Kind {
		case Text:
			if p.Text != "" {
				out = append(out, map[string]any{"type": "text", "text": p.Text})
			}
		case Thinking:
			out = append(out, map[string]any{"type": "thinking", "thinking": p.Text, "signature": p.Signature})
		case Image:
			if p.URL != "" {
				out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "url", "url": p.URL}})
			} else {
				out = append(out, map[string]any{"type": "image", "source": map[string]any{"type": "base64", "media_type": p.MediaType, "data": p.Data}})
			}
		case ToolCall:
			out = append(out, map[string]any{"type": "tool_use", "id": p.ID, "name": p.Name, "input": json.RawMessage(orRaw(p.Args))})
		case ToolResult:
			out = append(out, map[string]any{"type": "tool_result", "tool_use_id": p.CallID, "content": p.Text})
		}
	}
	if len(out) == 0 {
		out = append(out, map[string]any{"type": "text", "text": ""})
	}
	return out
}

func orInt(v, def int) int {
	if v > 0 {
		return v
	}
	return def
}

func orRaw(r json.RawMessage) json.RawMessage {
	if len(r) == 0 || !json.Valid(r) {
		return json.RawMessage("{}")
	}
	return r
}
