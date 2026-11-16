// Package protocol holds the unified intermediate representation (IR) every
// wire API is parsed into and rendered from, plus parsers and builders for the
// OpenAI Chat, OpenAI Responses, Anthropic Messages and Gemini protocols.
//
// The APIs are close cousins: a request is parsed into IR, and a reply (or a
// request for a different upstream) is produced from it. This keeps the number
// of translation paths linear (N parsers + N builders) instead of quadratic.
package protocol

import (
	"encoding/json"
	"fmt"
	"strings"
)

// Protocol is a wire API an agent or a provider speaks.
type Protocol string

const (
	Chat      Protocol = "chat"      // OpenAI Chat Completions
	Responses Protocol = "responses" // OpenAI Responses
	Anthropic Protocol = "anthropic" // Anthropic Messages
	Gemini    Protocol = "gemini"    // Google Gemini generateContent
)

// Kind is what a part of a message holds.
type Kind string

const (
	Text       Kind = "text"
	Image      Kind = "image"
	ToolCall   Kind = "tool_call"
	ToolResult Kind = "tool_result"
	Thinking   Kind = "thinking"
)

// Part is one block of a message.
type Part struct {
	Kind Kind

	Text string // text, thinking, or a tool result's output

	// image
	MediaType string
	Data      string // base64
	URL       string

	// tool_call
	ID   string
	Name string
	Args json.RawMessage

	// tool_result
	CallID  string
	IsError bool

	// thinking signature (Anthropic), carried through untouched
	Signature string
}

// Message is one turn.
type Message struct {
	Role  string // user | assistant | system | developer
	Parts []Part
}

// Tool is a function the model may call.
type Tool struct {
	Name        string
	Description string
	Schema      json.RawMessage
}

// Request is a call to a model, whichever API it arrived in, or is sent in.
type Request struct {
	Model    string
	System   []string // system/developer instructions, joined by the builder
	Messages []Message
	Tools    []Tool

	Stream    bool
	MaxTokens int
	Temp      *float64
	TopP      *float64
	Stop      []string
}

// Detect returns the protocol a request path + body speaks.
func Detect(path string) Protocol {
	switch {
	case strings.HasSuffix(path, "/messages"):
		return Anthropic
	case strings.HasSuffix(path, "/responses"):
		return Responses
	case strings.Contains(path, ":generateContent") || strings.Contains(path, ":streamGenerateContent"):
		return Gemini
	default:
		return Chat
	}
}

// UpstreamPath is where a provider that speaks proto serves its generative
// endpoint, given its base URL.
func UpstreamPath(base string, proto Protocol, model string, stream bool) string {
	switch proto {
	case Anthropic:
		return base + "/v1/messages"
	case Responses:
		return base + "/v1/responses"
	case Gemini:
		m := "generateContent"
		if stream {
			m = "streamGenerateContent"
		}
		return base + "/v1beta/models/" + model + ":" + m + "?alt=sse"
	default:
		return base + "/v1/chat/completions"
	}
}

// Parse parses body as proto into IR.
func Parse(proto Protocol, body []byte) (*Request, error) {
	switch proto {
	case Anthropic:
		return parseAnthropic(body)
	case Responses:
		return parseResponses(body)
	case Gemini:
		return parseGemini(body)
	default:
		return parseChat(body)
	}
}

// Build renders req as proto, as the body to send upstream or back to a
// client. model overrides req.Model when non-empty.
func Build(proto Protocol, req *Request, model string) ([]byte, error) {
	if model == "" {
		model = req.Model
	}
	switch proto {
	case Anthropic:
		return buildAnthropic(req, model)
	case Responses:
		return buildResponses(req, model)
	case Gemini:
		return buildGemini(req, model)
	default:
		return buildChat(req, model)
	}
}

func decode(body []byte, v any) error {
	if err := json.Unmarshal(body, v); err != nil {
		return fmt.Errorf("invalid json: %w", err)
	}
	return nil
}
