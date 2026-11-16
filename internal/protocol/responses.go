package protocol

import "encoding/json"

// ---- OpenAI Responses ----
// A pragmatic subset: input items (message / function_call / function_call_output),
// instructions, tools. Enough to translate to and from Chat/Anthropic.

type responsesReq struct {
	Model        string          `json:"model"`
	Instructions string          `json:"instructions"`
	Input        json.RawMessage `json:"input"` // string or []item
	Tools        []responsesTool `json:"tools"`
	Stream       bool            `json:"stream"`
	MaxTokens    int             `json:"max_output_tokens"`
	Temperature  *float64        `json:"temperature"`
	TopP         *float64        `json:"top_p"`
}

type responsesTool struct {
	Type        string          `json:"type"`
	Name        string          `json:"name"`
	Description string          `json:"description"`
	Parameters  json.RawMessage `json:"parameters"`
}

type respItem struct {
	Type    string          `json:"type,omitempty"`
	Role    string          `json:"role,omitempty"`
	Content json.RawMessage `json:"content,omitempty"` // string or []respPart
	// function_call
	CallID    string `json:"call_id,omitempty"`
	Name      string `json:"name,omitempty"`
	Arguments string `json:"arguments,omitempty"`
	// function_call_output
	Output string `json:"output,omitempty"`
}

type respPart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

func parseResponses(body []byte) (*Request, error) {
	var in responsesReq
	if err := decode(body, &in); err != nil {
		return nil, err
	}
	req := &Request{Model: in.Model, Stream: in.Stream, MaxTokens: in.MaxTokens, Temp: in.Temperature, TopP: in.TopP}
	if in.Instructions != "" {
		req.System = append(req.System, in.Instructions)
	}
	var s string
	if json.Unmarshal(in.Input, &s) == nil && s != "" {
		req.Messages = append(req.Messages, Message{Role: "user", Parts: []Part{{Kind: Text, Text: s}}})
	} else {
		var items []respItem
		if json.Unmarshal(in.Input, &items) == nil {
			for _, it := range items {
				switch it.Type {
				case "", "message":
					role := it.Role
					if role == "" || role == "developer" {
						role = it.Role
					}
					var texts []string
					var raw string
					if json.Unmarshal(it.Content, &raw) == nil {
						texts = []string{raw}
					} else {
						var parts []respPart
						if json.Unmarshal(it.Content, &parts) == nil {
							for _, p := range parts {
								if p.Text != "" {
									texts = append(texts, p.Text)
								}
							}
						}
					}
					if it.Role == "system" || it.Role == "developer" {
						req.System = append(req.System, texts...)
						continue
					}
					m := Message{Role: it.Role}
					if m.Role == "" {
						m.Role = "user"
					}
					for _, t := range texts {
						m.Parts = append(m.Parts, Part{Kind: Text, Text: t})
					}
					req.Messages = append(req.Messages, m)
				case "function_call":
					req.Messages = append(req.Messages, Message{Role: "assistant", Parts: []Part{{Kind: ToolCall, ID: it.CallID, Name: it.Name, Args: safeArgs(it.Arguments)}}})
				case "function_call_output":
					req.Messages = append(req.Messages, Message{Role: "user", Parts: []Part{{Kind: ToolResult, CallID: it.CallID, Text: it.Output}}})
				}
			}
		}
	}
	for _, t := range in.Tools {
		if t.Type != "" && t.Type != "function" {
			continue
		}
		req.Tools = append(req.Tools, Tool{Name: t.Name, Description: t.Description, Schema: t.Parameters})
	}
	return req, nil
}

func buildResponses(req *Request, model string) ([]byte, error) {
	var items []map[string]any
	for _, m := range req.Messages {
		if m.Role == "system" || m.Role == "developer" {
			continue
		}
		items = append(items, responsesItems(m)...)
	}
	out := map[string]any{"model": model, "input": items}
	if sys := join(req.System); sys != "" {
		out["instructions"] = sys
	}
	if req.Stream {
		out["stream"] = true
	}
	if req.MaxTokens > 0 {
		out["max_output_tokens"] = req.MaxTokens
	}
	if req.Temp != nil {
		out["temperature"] = *req.Temp
	}
	if len(req.Tools) > 0 {
		ts := []map[string]any{}
		for _, t := range req.Tools {
			ts = append(ts, map[string]any{"type": "function", "name": t.Name, "description": t.Description, "parameters": normalizeSchema(t.Schema)})
		}
		out["tools"] = ts
	}
	return json.Marshal(out)
}

func responsesItems(m Message) []map[string]any {
	var out []map[string]any
	var texts []string
	for _, p := range m.Parts {
		switch p.Kind {
		case Text:
			texts = append(texts, p.Text)
		case ToolCall:
			if len(texts) > 0 {
				out = append(out, respMessage(m.Role, texts))
				texts = nil
			}
			out = append(out, map[string]any{"type": "function_call", "call_id": p.ID, "name": p.Name, "arguments": string(p.Args)})
		case ToolResult:
			if len(texts) > 0 {
				out = append(out, respMessage(m.Role, texts))
				texts = nil
			}
			out = append(out, map[string]any{"type": "function_call_output", "call_id": p.CallID, "output": p.Text})
		}
	}
	if len(texts) > 0 {
		out = append(out, respMessage(m.Role, texts))
	}
	return out
}

func respMessage(role string, texts []string) map[string]any {
	if role == "" {
		role = "user"
	}
	parts := []map[string]any{}
	for _, t := range texts {
		parts = append(parts, map[string]any{"type": "input_text", "text": t})
	}
	return map[string]any{"type": "message", "role": role, "content": parts}
}
