package gateway

import (
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"time"

	"github.com/tidwall/gjson"

	"simple-magpie/internal/protocol"
	"simple-magpie/internal/store"
	"simple-magpie/internal/transcript"
)

func itoa(i int64) string { return strconv.FormatInt(i, 10) }

// renderReply renders an IR reply as protocol proto, one JSON body.
func renderReply(proto protocol.Protocol, r Reply) []byte {
	switch proto {
	case protocol.Anthropic:
		var content []map[string]any
		for _, c := range r.Calls {
			content = append(content, map[string]any{"type": "tool_use", "id": c.ID, "name": c.Name, "input": rawJSON(c.Args)})
		}
		if r.Text != "" {
			content = append([]map[string]any{{"type": "text", "text": r.Text}}, content...)
		}
		return mustJSON(map[string]any{
			"id": "msg_simple", "type": "message", "role": "assistant", "model": "simple",
			"content": content, "stop_reason": stopOf(r, "end_turn"), "stop_sequence": nil,
			"usage": map[string]any{"input_tokens": r.Usage.Input, "output_tokens": r.Usage.Output,
				"cache_read_input_tokens": r.Usage.CacheRead, "cache_creation_input_tokens": r.Usage.CacheWrite},
		})
	case protocol.Gemini:
		var parts []map[string]any
		if r.Text != "" {
			parts = append(parts, map[string]any{"text": r.Text})
		}
		for _, c := range r.Calls {
			parts = append(parts, map[string]any{"functionCall": map[string]any{"name": c.Name, "args": rawJSON(c.Args)}})
		}
		return mustJSON(map[string]any{
			"candidates":    []map[string]any{{"content": map[string]any{"role": "model", "parts": parts}, "finishReason": "STOP"}},
			"usageMetadata": map[string]any{"promptTokenCount": r.Usage.Input, "candidatesTokenCount": r.Usage.Output, "cachedContentTokenCount": r.Usage.CacheRead},
		})
	case protocol.Responses:
		var out []map[string]any
		if r.Text != "" {
			out = append(out, map[string]any{"type": "message", "role": "assistant", "content": []map[string]any{{"type": "output_text", "text": r.Text}}})
		}
		for _, c := range r.Calls {
			out = append(out, map[string]any{"type": "function_call", "call_id": c.ID, "name": c.Name, "arguments": c.Args})
		}
		return mustJSON(map[string]any{
			"id": "resp_simple", "object": "response", "status": "completed", "output": out,
			"usage": map[string]any{"input_tokens": r.Usage.Input, "output_tokens": r.Usage.Output,
				"input_tokens_details": map[string]any{"cached_tokens": r.Usage.CacheRead}},
		})
	default: // chat
		msg := map[string]any{"role": "assistant", "content": r.Text}
		if len(r.Calls) > 0 {
			tcs := []map[string]any{}
			for _, c := range r.Calls {
				tcs = append(tcs, map[string]any{"id": c.ID, "type": "function", "function": map[string]any{"name": c.Name, "arguments": c.Args}})
			}
			msg["tool_calls"] = tcs
		}
		return mustJSON(map[string]any{
			"id": "chatcmpl-simple", "object": "chat.completion", "created": time.Now().Unix(), "model": "simple",
			"choices": []map[string]any{{"index": 0, "message": msg, "finish_reason": stopOf(r, "stop")}},
			"usage": map[string]any{"prompt_tokens": r.Usage.Input, "completion_tokens": r.Usage.Output,
				"prompt_tokens_details": map[string]any{"cached_tokens": r.Usage.CacheRead}},
		})
	}
}

func stopOf(r Reply, def string) string {
	if len(r.Calls) > 0 {
		return "tool_use"
	}
	return def
}

// writeTranslatedStream writes an IR reply as an SSE stream in protocol proto.
func writeTranslatedStream(w http.ResponseWriter, proto protocol.Protocol, r Reply) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	f, _ := w.(http.Flusher)
	switch proto {
	case protocol.Anthropic:
		writeSSE(w, f, "message_start", map[string]any{"type": "message_start", "message": map[string]any{
			"id": "msg_simple", "type": "message", "role": "assistant", "content": []any{},
			"usage": map[string]any{"input_tokens": r.Usage.Input, "cache_read_input_tokens": r.Usage.CacheRead}}})
		writeSSE(w, f, "content_block_start", map[string]any{"type": "content_block_start", "index": 0, "content_block": map[string]any{"type": "text", "text": ""}})
		writeSSE(w, f, "content_block_delta", map[string]any{"type": "content_block_delta", "index": 0, "delta": map[string]any{"type": "text_delta", "text": r.Text}})
		writeSSE(w, f, "content_block_stop", map[string]any{"type": "content_block_stop", "index": 0})
		writeSSE(w, f, "message_delta", map[string]any{"type": "message_delta", "delta": map[string]any{"stop_reason": stopOf(r, "end_turn")}, "usage": map[string]any{"output_tokens": r.Usage.Output}})
		writeSSE(w, f, "message_stop", map[string]any{"type": "message_stop"})
	case protocol.Responses:
		writeSSE(w, f, "response.output_text.delta", map[string]any{"type": "response.output_text.delta", "delta": r.Text})
		writeSSE(w, f, "response.completed", map[string]any{"type": "response.completed", "response": json.RawMessage(renderReply(protocol.Responses, r))})
	case protocol.Gemini:
		b := renderReply(protocol.Gemini, r)
		fmt.Fprintf(w, "data: %s\n\n", b)
		if f != nil {
			f.Flush()
		}
	default: // chat
		writeSSE(w, f, "", map[string]any{"id": "chatcmpl-simple", "object": "chat.completion.chunk", "created": time.Now().Unix(),
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{"role": "assistant", "content": r.Text}, "finish_reason": nil}}})
		writeSSE(w, f, "", map[string]any{"id": "chatcmpl-simple", "object": "chat.completion.chunk",
			"choices": []map[string]any{{"index": 0, "delta": map[string]any{}, "finish_reason": stopOf(r, "stop")}}})
		// usage-only final chunk
		writeSSE(w, f, "", map[string]any{"id": "chatcmpl-simple", "object": "chat.completion.chunk", "choices": []any{},
			"usage": map[string]any{"prompt_tokens": r.Usage.Input, "completion_tokens": r.Usage.Output}})
	}
	fmt.Fprint(w, "data: [DONE]\n\n")
	if f != nil {
		f.Flush()
	}
}

func writeSSE(w http.ResponseWriter, f http.Flusher, event string, v any) {
	if event != "" {
		fmt.Fprintf(w, "event: %s\n", event)
	}
	fmt.Fprintf(w, "data: %s\n\n", mustJSON(v))
	if f != nil {
		f.Flush()
	}
}

func mustJSON(v any) []byte {
	if raw, ok := v.(json.RawMessage); ok {
		return raw
	}
	b, _ := json.Marshal(v)
	return b
}

func rawJSON(s string) json.RawMessage {
	if s == "" || !json.Valid([]byte(s)) {
		return json.RawMessage("{}")
	}
	return json.RawMessage(s)
}

// usageOf reads usage from a whole JSON reply in protocol from.
func usageOf(from protocol.Protocol, b []byte, stream bool) protocolUsage {
	_, _, _, _, u := parseReplyJSON(from, b)
	if u.Input > 0 || u.Output > 0 || u.CacheRead > 0 || u.CacheWrite > 0 {
		u.Known = true
	}
	return u
}

// collectUsage folds a streamed event's usage into rec.
func collectUsage(from protocol.Protocol, data []byte, rec *store.Record) {
	text, calls, think, u := parseReplyDelta(from, data)
	// accumulate the visible answer for the conversation view
	if text != "" {
		rec.ReplyText += text
	}
	if think != "" {
		rec.ReplyText += think
	}
	for _, c := range calls {
		rec.ReplyCalls = mergeReplyCalls(rec.ReplyCalls, c)
	}
	if u.Input == 0 && u.Output == 0 && u.CacheRead == 0 && u.CacheWrite == 0 {
		return
	}
	if u.Input > 0 {
		rec.Usage.Input = u.Input
	}
	if u.Output > 0 {
		rec.Usage.Output = u.Output
	}
	if u.CacheRead > 0 {
		rec.Usage.CacheRead = u.CacheRead
	}
	if u.CacheWrite > 0 {
		rec.Usage.CacheWrite = u.CacheWrite
	}
	rec.Usage.Known = true
	rec.CacheHit = rec.Usage.CacheHit()
}

// mergeReplyCalls folds a streamed tool-call delta into the accumulated calls.
func mergeReplyCalls(calls []transcript.ReplyCall, d Call) []transcript.ReplyCall {
	for i := range calls {
		if calls[i].ID != "" && calls[i].ID == d.ID {
			calls[i].Args += d.Args
			if d.Name != "" {
				calls[i].Name = d.Name
			}
			return calls
		}
	}
	return append(calls, transcript.ReplyCall{ID: d.ID, Name: d.Name, Args: d.Args})
}

var _ = gjson.GetBytes
