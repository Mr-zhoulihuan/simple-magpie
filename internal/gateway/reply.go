package gateway

import (
	"bufio"
	"bytes"
	"encoding/json"
	"io"
	"net/http"
	"strings"

	"github.com/tidwall/gjson"

	"simple-magpie/internal/prompt"
	"simple-magpie/internal/protocol"
	"simple-magpie/internal/store"
	"simple-magpie/internal/transcript"
)

// relay passes a same-API reply through unchanged, flushing as it streams, and
// reads the usage from it for the record.
func (s *Server) relay(res *http.Response, w http.ResponseWriter, from protocol.Protocol, rec *store.Record) {
	ct := res.Header.Get("Content-Type")
	for k, vs := range res.Header {
		lk := strings.ToLower(k)
		if isHop(lk) || lk == "content-length" {
			continue
		}
		for _, v := range vs {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(res.StatusCode)
	flusher, _ := w.(http.Flusher)

	if strings.Contains(ct, "text/event-stream") {
		s.relaySSE(res.Body, w, flusher, from, rec)
		return
	}
	// one JSON body
	b, err := io.ReadAll(res.Body)
	if err != nil {
		return
	}
	rec.Usage = usageOf(from, b, rec.Streamed)
	rec.CacheHit = rec.Usage.CacheHit()
	// capture the answer for the conversation view
	if t, calls, _, _, _ := parseReplyJSON(from, b); true {
		rec.ReplyText = t
		for _, c := range calls {
			rec.ReplyCalls = append(rec.ReplyCalls, transcript.ReplyCall{ID: c.ID, Name: c.Name, Args: c.Args})
		}
	}
	w.Write(b)
	if flusher != nil {
		flusher.Flush()
	}
}

// relaySSE forwards an event stream as-is and reads usage from the terminal
// events.
func (s *Server) relaySSE(r io.Reader, w http.ResponseWriter, flusher http.Flusher, from protocol.Protocol, rec *store.Record) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	var dataBuf bytes.Buffer
	for sc.Scan() {
		line := sc.Bytes()
		w.Write(line)
		w.Write([]byte("\n"))
		if flusher != nil {
			flusher.Flush()
		}
		if bytes.HasPrefix(line, []byte("data:")) {
			data := bytes.TrimSpace(line[len("data:"):])
			if len(data) > 0 && data[0] == '{' {
				dataBuf.Write(data)
				collectUsage(from, data, rec)
			}
		}
	}
}

// translateReply converts a different-API reply into the client's API.
func (s *Server) translateReply(res *http.Response, w http.ResponseWriter, from, to protocol.Protocol, rec *store.Record) {
	ct := res.Header.Get("Content-Type")
	stream := strings.Contains(ct, "text/event-stream") || rec.Streamed

	// gather the whole upstream reply first (simple, correct; a full build
	// streams event-by-event). Then render it in `from`.
	text, calls, usage, think, err := gatherReply(to, res.Body, stream)
	if err != nil {
		writeError(w, from, 502, err.Error())
		return
	}
	rec.Usage = usage
	rec.CacheHit = usage.CacheHit()
	rec.ReplyText = text + think
	for _, c := range calls {
		rec.ReplyCalls = append(rec.ReplyCalls, transcript.ReplyCall{ID: c.ID, Name: c.Name, Args: c.Args})
	}
	reply := Reply{Text: text, Calls: calls, Thinking: think, Usage: usage}

	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(200)
	if rec.Streamed {
		writeTranslatedStream(w, from, reply)
	} else {
		w.Write(renderReply(from, reply))
	}
	if f, ok := w.(http.Flusher); ok {
		f.Flush()
	}
}

// Reply is an IR-level answer.
type Reply struct {
	Text     string
	Thinking string
	Calls    []Call
	Usage    protocolUsage
	Stop     string
}

// Call is a tool call in a reply.
type Call struct {
	ID   string
	Name string
	Args string
}

// gatherReply reads a whole reply (or an SSE stream) in protocol to.
func gatherReply(to protocol.Protocol, r io.Reader, stream bool) (string, []Call, protocolUsage, string, error) {
	u := protocolUsage{}
	if !stream {
		b, err := io.ReadAll(r)
		if err != nil {
			return "", nil, u, "", err
		}
		text, calls, think, _, uu := parseReplyJSON(to, b)
		adopt(&u, uu)
		return text, calls, u, think, nil
	}
	var text strings.Builder
	var think strings.Builder
	var calls []Call
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 64*1024), 8*1024*1024)
	for sc.Scan() {
		line := sc.Bytes()
		if !bytes.HasPrefix(line, []byte("data:")) {
			continue
		}
		data := bytes.TrimSpace(line[len("data:"):])
		if len(data) == 0 || data[0] != '{' {
			continue
		}
		t, c, th, uu := parseReplyDelta(to, data)
		text.WriteString(t)
		think.WriteString(th)
		calls = mergeCalls(calls, c)
		adopt(&u, uu)
	}
	return text.String(), calls, u, think.String(), nil
}

// mergeCalls appends streaming tool-call deltas by id.
func mergeCalls(calls []Call, deltas []Call) []Call {
	for _, d := range deltas {
		found := false
		for i := range calls {
			if calls[i].ID == d.ID && d.ID != "" {
				calls[i].Args += d.Args
				if d.Name != "" {
					calls[i].Name = d.Name
				}
				found = true
				break
			}
		}
		if !found {
			calls = append(calls, d)
		}
	}
	return calls
}

// protocolUsage is a vendor's token accounting read from a reply, kept equal to
// prompt.Usage so the record carries one shape.
type protocolUsage = prompt.Usage

func adopt(u *protocolUsage, o protocolUsage) {
	if o.Input > 0 || o.Output > 0 || o.CacheRead > 0 || o.CacheWrite > 0 {
		u.Input, u.Output, u.CacheRead, u.CacheWrite = o.Input, o.Output, o.CacheRead, o.CacheWrite
		u.Known = true
	}
}

// ---- per-protocol reply parsing ----

func parseReplyJSON(to protocol.Protocol, b []byte) (string, []Call, string, string, protocolUsage) {
	u := protocolUsage{}
	switch to {
	case protocol.Anthropic:
		var v struct {
			Content []struct {
				Type     string          `json:"type"`
				Text     string          `json:"text"`
				Thinking string          `json:"thinking"`
				ID       string          `json:"id"`
				Name     string          `json:"name"`
				Input    json.RawMessage `json:"input"`
			} `json:"content"`
			StopReason string `json:"stop_reason"`
			Usage      struct {
				InputTokens              int `json:"input_tokens"`
				OutputTokens             int `json:"output_tokens"`
				CacheReadInputTokens     int `json:"cache_read_input_tokens"`
				CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
			} `json:"usage"`
		}
		json.Unmarshal(b, &v)
		var text, think string
		var calls []Call
		for _, c := range v.Content {
			switch c.Type {
			case "text":
				text += c.Text
			case "thinking":
				think += c.Thinking
			case "tool_use":
				calls = append(calls, Call{ID: c.ID, Name: c.Name, Args: string(c.Input)})
			}
		}
		u.Input, u.Output = v.Usage.InputTokens, v.Usage.OutputTokens
		u.CacheRead, u.CacheWrite = v.Usage.CacheReadInputTokens, v.Usage.CacheCreationInputTokens
		return text, calls, think, v.StopReason, u
	case protocol.Gemini:
		var v struct {
			Candidates []struct {
				Content struct {
					Parts []struct {
						Text         string `json:"text"`
						FunctionCall *struct {
							Name string          `json:"name"`
							Args json.RawMessage `json:"args"`
						} `json:"functionCall"`
					} `json:"parts"`
				} `json:"content"`
				FinishReason string `json:"finishReason"`
			} `json:"candidates"`
			UsageMetadata struct {
				PromptTokenCount     int `json:"promptTokenCount"`
				CandidatesTokenCount int `json:"candidatesTokenCount"`
				CachedContentCount   int `json:"cachedContentTokenCount"`
			} `json:"usageMetadata"`
		}
		json.Unmarshal(b, &v)
		var text string
		var calls []Call
		for _, c := range v.Candidates {
			for _, p := range c.Content.Parts {
				text += p.Text
				if p.FunctionCall != nil {
					calls = append(calls, Call{ID: "call_" + p.FunctionCall.Name, Name: p.FunctionCall.Name, Args: string(p.FunctionCall.Args)})
				}
			}
		}
		u.Input, u.Output, u.CacheRead = v.UsageMetadata.PromptTokenCount, v.UsageMetadata.CandidatesTokenCount, v.UsageMetadata.CachedContentCount
		return text, calls, "", "", u
	case protocol.Responses:
		var v struct {
			Output []struct {
				Type    string `json:"type"`
				Content []struct {
					Type string `json:"type"`
					Text string `json:"text"`
				} `json:"content"`
				CallID    string `json:"call_id"`
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			} `json:"output"`
			Usage struct {
				InputTokens  int `json:"input_tokens"`
				OutputTokens int `json:"output_tokens"`
				InputDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"input_tokens_details"`
			} `json:"usage"`
		}
		json.Unmarshal(b, &v)
		var text string
		var calls []Call
		for _, o := range v.Output {
			for _, c := range o.Content {
				text += c.Text
			}
			if o.Type == "function_call" {
				calls = append(calls, Call{ID: o.CallID, Name: o.Name, Args: o.Arguments})
			}
		}
		u.Input, u.Output, u.CacheRead = v.Usage.InputTokens, v.Usage.OutputTokens, v.Usage.InputDetails.CachedTokens
		return text, calls, "", "", u
	default: // chat
		var v struct {
			Choices []struct {
				Message struct {
					Content   string `json:"content"`
					ToolCalls []struct {
						ID       string `json:"id"`
						Function struct {
							Name      string `json:"name"`
							Arguments string `json:"arguments"`
						} `json:"function"`
					} `json:"tool_calls"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			} `json:"choices"`
			Usage struct {
				PromptTokens        int `json:"prompt_tokens"`
				CompletionTokens    int `json:"completion_tokens"`
				PromptTokensDetails struct {
					CachedTokens int `json:"cached_tokens"`
				} `json:"prompt_tokens_details"`
			} `json:"usage"`
		}
		json.Unmarshal(b, &v)
		var text string
		var calls []Call
		if len(v.Choices) > 0 {
			text = v.Choices[0].Message.Content
			for _, tc := range v.Choices[0].Message.ToolCalls {
				calls = append(calls, Call{ID: tc.ID, Name: tc.Function.Name, Args: tc.Function.Arguments})
			}
		}
		u.Input, u.Output, u.CacheRead = v.Usage.PromptTokens, v.Usage.CompletionTokens, v.Usage.PromptTokensDetails.CachedTokens
		return text, calls, "", "", u
	}
}

// parseReplyDelta parses one streamed event in protocol to.
func parseReplyDelta(to protocol.Protocol, data []byte) (string, []Call, string, protocolUsage) {
	u := protocolUsage{}
	switch to {
	case protocol.Anthropic:
		t := gjson.GetBytes(data, "type").String()
		if t == "content_block_delta" {
			d := gjson.GetBytes(data, "delta")
			switch d.Get("type").String() {
			case "text_delta":
				return d.Get("text").String(), nil, "", u
			case "thinking_delta":
				return "", nil, d.Get("thinking").String(), u
			case "input_json_delta":
				idx := gjson.GetBytes(data, "index").Int()
				return "", []Call{{ID: itoa(idx), Args: d.Get("partial_json").String()}}, "", u
			}
		}
		if t == "message_start" {
			uu := gjson.GetBytes(data, "message.usage")
			u.Input = int(uu.Get("input_tokens").Int())
			u.CacheRead = int(uu.Get("cache_read_input_tokens").Int())
			u.CacheWrite = int(uu.Get("cache_creation_input_tokens").Int())
		}
		if t == "message_delta" {
			u.Output = int(gjson.GetBytes(data, "usage.output_tokens").Int())
		}
	case protocol.Gemini:
		p := gjson.GetBytes(data, "candidates.0.content.parts")
		var text string
		var calls []Call
		p.ForEach(func(_, v gjson.Result) bool {
			text += v.Get("text").String()
			if fc := v.Get("functionCall"); fc.Exists() {
				calls = append(calls, Call{ID: "call_" + fc.Get("name").String(), Name: fc.Get("name").String(), Args: fc.Get("args").Raw})
			}
			return true
		})
		um := gjson.GetBytes(data, "usageMetadata")
		u.Input = int(um.Get("promptTokenCount").Int())
		u.Output = int(um.Get("candidatesTokenCount").Int())
		u.CacheRead = int(um.Get("cachedContentTokenCount").Int())
		return text, calls, "", u
	case protocol.Responses:
		t := gjson.GetBytes(data, "type").String()
		if t == "response.output_text.delta" {
			return gjson.GetBytes(data, "delta").String(), nil, "", u
		}
		if t == "response.function_call_arguments.delta" {
			return "", []Call{{ID: gjson.GetBytes(data, "item_id").String(), Args: gjson.GetBytes(data, "delta").String()}}, "", u
		}
		if strings.HasPrefix(t, "response.completed") || t == "response.incomplete" {
			uu := gjson.GetBytes(data, "response.usage")
			u.Input = int(uu.Get("input_tokens").Int())
			u.Output = int(uu.Get("output_tokens").Int())
			u.CacheRead = int(uu.Get("input_tokens_details.cached_tokens").Int())
		}
	default: // chat
		ch := gjson.GetBytes(data, "choices.0.delta")
		var text string
		var calls []Call
		if c := ch.Get("content"); c.Exists() && c.Type == gjson.String {
			text = c.String()
		}
		ch.Get("tool_calls").ForEach(func(_, v gjson.Result) bool {
			id := v.Get("id").String()
			if id == "" {
				// a continuation chunk names the call by index only
				id = "idx:" + v.Get("index").String()
			}
			calls = append(calls, Call{ID: id, Name: v.Get("function.name").String(), Args: v.Get("function.arguments").String()})
			return true
		})
		us := gjson.GetBytes(data, "usage")
		u.Input = int(us.Get("prompt_tokens").Int())
		u.Output = int(us.Get("completion_tokens").Int())
		u.CacheRead = int(us.Get("prompt_tokens_details.cached_tokens").Int())
		return text, calls, "", u
	}
	return "", nil, "", u
}
