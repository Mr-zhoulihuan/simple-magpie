// Package transcript turns a request's IR plus the reply simple-magpie produced
// into a displayable conversation: user/assistant messages, tool calls and
// their results, so the dashboard can show what the agent actually said and did.
package transcript

import (
	"time"

	"simple-magpie/internal/protocol"
)

// Turn is one visible item in the conversation.
type Turn struct {
	Role string `json:"role"` // user | assistant | system
	Kind string `json:"kind"` // text | tool_call | tool_result | thinking
	Text string `json:"text,omitempty"`
	// tool_call
	CallID string `json:"callId,omitempty"`
	Name   string `json:"name,omitempty"`
	Args   string `json:"args,omitempty"`
	// tool_result
	IsError bool `json:"isError,omitempty"`
	// trunc marks a Text cut to maxText bytes
	Trunc bool `json:"trunc,omitempty"`
	// Time is when this turn was seen (the request that first carried it).
	Time time.Time `json:"time,omitempty"`
}

// maxText bounds one text field so a huge tool output does not bloat memory or
// the page. The dashboard notes when a turn was cut.
const maxText = 8 * 1024

// ReplyTurn is what the assistant answered this request, appended as the
// conversation's newest turn(s).
type ReplyTurn struct {
	Text  string
	Calls []ReplyCall
}

// ReplyCall is one tool call the assistant made.
type ReplyCall struct {
	ID   string
	Name string
	Args string
}

// FromRequest renders the conversation a request carried, part by part, in the
// order the agent sent it. The system prompt is included as one turn.
func FromRequest(req *protocol.Request) []Turn {
	var out []Turn
	if len(req.System) > 0 {
		out = append(out, Turn{Role: "system", Kind: "text", Text: clip(join(req.System))})
	}
	for _, m := range req.Messages {
		role := m.Role
		switch role {
		case "system", "developer":
			role = "system"
		case "model":
			role = "assistant"
		}
		for _, p := range m.Parts {
			switch p.Kind {
			case protocol.Text:
				if p.Text != "" {
					out = append(out, textTurn(role, p.Text))
				}
			case protocol.Thinking:
				if p.Text != "" {
					out = append(out, Turn{Role: "assistant", Kind: "thinking", Text: clip(p.Text)})
				}
			case protocol.ToolCall:
				out = append(out, Turn{Role: "assistant", Kind: "tool_call", CallID: p.ID, Name: p.Name, Args: clip(string(p.Args))})
			case protocol.ToolResult:
				out = append(out, Turn{Role: resultRole(m.Role), Kind: "tool_result", CallID: p.CallID, Text: clip(p.Text), IsError: p.IsError})
			case protocol.Image:
				out = append(out, Turn{Role: role, Kind: "text", Text: "[image]"})
			}
		}
	}
	return out
}

// AppendReply appends the assistant's answer for this request.
func AppendReply(turns []Turn, r ReplyTurn) []Turn {
	if r.Text != "" {
		turns = append(turns, textTurn("assistant", r.Text))
	}
	for _, c := range r.Calls {
		turns = append(turns, Turn{Role: "assistant", Kind: "tool_call", CallID: c.ID, Name: c.Name, Args: clip(c.Args)})
	}
	return turns
}

func resultRole(role string) string {
	// a tool result is shown as the tool's output, authored by the tool
	return "tool"
}

func textTurn(role, text string) Turn {
	t := Turn{Role: role, Kind: "text", Text: clip(text)}
	return t
}

func clip(s string) string {
	if len(s) > maxText {
		return s[:maxText]
	}
	return s
}

func join(s []string) string {
	switch len(s) {
	case 0:
		return ""
	case 1:
		return s[0]
	}
	out := s[0]
	for _, x := range s[1:] {
		out += "\n\n" + x
	}
	return out
}
