package protocol

import "encoding/json"

// ---- Google Gemini generateContent ----

type geminiReq struct {
	Contents          []geminiContent `json:"contents"`
	SystemInstruction *geminiContent  `json:"systemInstruction,omitempty"`
	Tools             []geminiTool    `json:"tools,omitempty"`
	GenerationConfig  *struct {
		MaxOutputTokens int      `json:"maxOutputTokens"`
		Temperature     *float64 `json:"temperature"`
		TopP            *float64 `json:"topP"`
	} `json:"generationConfig,omitempty"`
}

type geminiContent struct {
	Role  string       `json:"role"` // user | model
	Parts []geminiPart `json:"parts"`
}

type geminiPart struct {
	Text         string `json:"text,omitempty"`
	FunctionCall *struct {
		Name string          `json:"name"`
		Args json.RawMessage `json:"args"`
	} `json:"functionCall,omitempty"`
	FunctionResponse *struct {
		Name     string          `json:"name"`
		Response json.RawMessage `json:"response"`
	} `json:"functionResponse,omitempty"`
	InlineData *struct {
		MimeType string `json:"mimeType"`
		Data     string `json:"data"`
	} `json:"inlineData,omitempty"`
}

type geminiTool struct {
	FunctionDeclarations []struct {
		Name        string          `json:"name"`
		Description string          `json:"description"`
		Parameters  json.RawMessage `json:"parameters"`
	} `json:"functionDeclarations"`
}

func parseGemini(body []byte) (*Request, error) {
	var in geminiReq
	if err := decode(body, &in); err != nil {
		return nil, err
	}
	req := &Request{}
	if in.GenerationConfig != nil {
		req.MaxTokens = in.GenerationConfig.MaxOutputTokens
		req.Temp = in.GenerationConfig.Temperature
		req.TopP = in.GenerationConfig.TopP
	}
	if in.SystemInstruction != nil {
		var texts []string
		for _, p := range in.SystemInstruction.Parts {
			if p.Text != "" {
				texts = append(texts, p.Text)
			}
		}
		if len(texts) > 0 {
			req.System = append(req.System, join(texts))
		}
	}
	callNames := map[string]string{}
	for _, c := range in.Contents {
		role := c.Role
		if role == "model" {
			role = "assistant"
		}
		m := Message{Role: role}
		for _, p := range c.Parts {
			switch {
			case p.Text != "":
				m.Parts = append(m.Parts, Part{Kind: Text, Text: p.Text})
			case p.FunctionCall != nil:
				id := "call_" + p.FunctionCall.Name
				callNames[string(p.FunctionCall.Args)] = p.FunctionCall.Name
				m.Parts = append(m.Parts, Part{Kind: ToolCall, ID: id, Name: p.FunctionCall.Name, Args: p.FunctionCall.Args})
			case p.FunctionResponse != nil:
				m.Parts = append(m.Parts, Part{Kind: ToolResult, CallID: "call_" + p.FunctionResponse.Name, Text: string(p.FunctionResponse.Response)})
			case p.InlineData != nil:
				m.Parts = append(m.Parts, Part{Kind: Image, MediaType: p.InlineData.MimeType, Data: p.InlineData.Data})
			}
		}
		req.Messages = append(req.Messages, m)
	}
	for _, t := range in.Tools {
		for _, fd := range t.FunctionDeclarations {
			req.Tools = append(req.Tools, Tool{Name: fd.Name, Description: fd.Description, Schema: fd.Parameters})
		}
	}
	return req, nil
}

func buildGemini(req *Request, model string) ([]byte, error) {
	var contents []map[string]any
	for _, m := range req.Messages {
		role := m.Role
		if role == "assistant" {
			role = "model"
		}
		if role == "system" || role == "developer" {
			continue
		}
		var parts []map[string]any
		for _, p := range m.Parts {
			switch p.Kind {
			case Text:
				if p.Text != "" {
					parts = append(parts, map[string]any{"text": p.Text})
				}
			case Image:
				parts = append(parts, map[string]any{"inlineData": map[string]any{"mimeType": p.MediaType, "data": p.Data}})
			case ToolCall:
				parts = append(parts, map[string]any{"functionCall": map[string]any{"name": p.Name, "args": orRaw(p.Args)}})
			case ToolResult:
				parts = append(parts, map[string]any{"functionResponse": map[string]any{"name": toolNameForResult(req, p.CallID), "response": map[string]any{"result": p.Text}}})
			}
		}
		if len(parts) == 0 {
			parts = append(parts, map[string]any{"text": ""})
		}
		contents = append(contents, map[string]any{"role": role, "parts": parts})
	}
	out := map[string]any{"contents": contents}
	if sys := join(req.System); sys != "" {
		out["systemInstruction"] = map[string]any{"parts": []map[string]any{{"text": sys}}}
	}
	if req.MaxTokens > 0 || req.Temp != nil || req.TopP != nil {
		gc := map[string]any{}
		if req.MaxTokens > 0 {
			gc["maxOutputTokens"] = req.MaxTokens
		}
		if req.Temp != nil {
			gc["temperature"] = *req.Temp
		}
		if req.TopP != nil {
			gc["topP"] = *req.TopP
		}
		out["generationConfig"] = gc
	}
	if len(req.Tools) > 0 {
		var decls []map[string]any
		for _, t := range req.Tools {
			decls = append(decls, map[string]any{"name": t.Name, "description": t.Description, "parameters": normalizeSchema(t.Schema)})
		}
		out["tools"] = []map[string]any{{"functionDeclarations": decls}}
	}
	return json.Marshal(out)
}

func toolNameForResult(req *Request, callID string) string {
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			if p.Kind == ToolCall && p.ID == callID {
				return p.Name
			}
		}
	}
	return callID
}
