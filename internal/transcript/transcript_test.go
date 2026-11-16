package transcript

import (
	"testing"

	"simple-magpie/internal/protocol"
)

func TestFromRequestToolCallAndResult(t *testing.T) {
	req := &protocol.Request{
		System: []string{"you are pi"},
		Messages: []protocol.Message{
			{Role: "user", Parts: []protocol.Part{{Kind: protocol.Text, Text: "run echo"}}},
			{Role: "assistant", Parts: []protocol.Part{{Kind: protocol.ToolCall, ID: "c1", Name: "bash", Args: []byte(`{"command":"echo hi"}`)}}},
			{Role: "user", Parts: []protocol.Part{{Kind: protocol.ToolResult, CallID: "c1", Text: "hi"}}},
		},
	}
	turns := FromRequest(req)
	if len(turns) != 4 {
		t.Fatalf("turns = %d, want 4 (system,user,call,result)", len(turns))
	}
	if turns[0].Role != "system" || turns[0].Kind != "text" {
		t.Errorf("turn0 = %+v", turns[0])
	}
	if turns[2].Kind != "tool_call" || turns[2].Name != "bash" || turns[2].CallID != "c1" {
		t.Errorf("tool_call turn = %+v", turns[2])
	}
	if turns[3].Kind != "tool_result" || turns[3].CallID != "c1" || turns[3].Text != "hi" {
		t.Errorf("tool_result turn = %+v", turns[3])
	}
}

func TestAppendReply(t *testing.T) {
	turns := AppendReply(nil, ReplyTurn{
		Text:  "done",
		Calls: []ReplyCall{{ID: "c2", Name: "read", Args: `{"path":"x"}`}},
	})
	if len(turns) != 2 {
		t.Fatalf("turns = %d, want 2 (text + call)", len(turns))
	}
	if turns[0].Text != "done" || turns[1].Kind != "tool_call" || turns[1].Name != "read" {
		t.Fatalf("reply turns = %+v", turns)
	}
}

func TestClip(t *testing.T) {
	big := make([]byte, maxText+100)
	for i := range big {
		big[i] = 'a'
	}
	got := clip(string(big))
	if len(got) != maxText {
		t.Fatalf("clip length = %d, want %d", len(got), maxText)
	}
}
