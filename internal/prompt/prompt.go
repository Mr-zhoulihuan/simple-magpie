// Package prompt analyzes a request's prompt: how many tokens it holds, what
// it is made of (system prompt, tool definitions, memory files, files the
// tools read, tool results, the conversation), the prompt cache read, and the
// headroom left — the numbers magpie's Context Window page draws.
package prompt

import (
	"sort"
	"strings"
	"unicode/utf8"

	"simple-magpie/internal/protocol"
)

// The kinds of a prompt's parts, in the order they are shown.
const (
	PartSystem  = "system"  // the agent's own prompt: system/developer messages
	PartTools   = "tools"   // tool definitions
	PartMemory  = "memory"  // instruction files, reminders (AGENTS.md, CLAUDE.md…)
	PartFiles   = "files"   // files and images the tools read in
	PartResults = "results" // the tools' other output (shell, bash…)
	PartChat    = "chat"    // what the user and the model said, the model's calls
)

// Order is the display order of parts.
var Order = []string{PartSystem, PartTools, PartMemory, PartFiles, PartResults, PartChat}

// Prompt is a request's prompt, part by part.
type Prompt struct {
	Window  int          `json:"window"`  // context window of the answering model
	Tokens  int          `json:"tokens"`  // prompt tokens (estimated or counted)
	Counted bool         `json:"counted"` // true when the vendor's usage set Tokens
	Turns   int          `json:"turns"`   // the user's turns in it
	Parts   []PromptPart `json:"parts"`
}

// PromptPart is one kind of what a prompt holds.
type PromptPart struct {
	Kind   string       `json:"kind"`
	Tokens int          `json:"tokens"`
	Items  []PromptItem `json:"items,omitempty"` // largest first, capped
}

// PromptItem is one thing in a part: a tool, an instruction file, a file read,
// a turn.
type PromptItem struct {
	Name   string `json:"name"`
	Tag    string `json:"tag,omitempty"` // what it is: memory, read, shell, turn…
	N      int    `json:"n,omitempty"`   // how many it gathers (bash ×31)
	Tokens int    `json:"tokens"`
	// Text is the item's own content, bounded (itemTextCap bytes), so the
	// dashboard can show what an item actually held when clicked. Truncated
	// reports it was cut.
	Text      string `json:"text,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`
}

// itemTextCap bounds the per-item content kept for the dashboard.
const itemTextCap = 8 * 1024

// maxItems is how many items a part keeps; the rest are summed in one.
const maxItems = 24

// imageTokens is what an image is taken to cost.
const imageTokens = 1600

// TokensOf estimates a text's tokens: about four characters a token for ASCII,
// a token a character for the rest (CJK, mostly).
func TokensOf(s string) int {
	ascii, other := 0, 0
	for i := 0; i < len(s); {
		if s[i] < utf8.RuneSelf {
			ascii++
			i++
			continue
		}
		_, n := utf8.DecodeRuneInString(s[i:])
		other++
		i += n
	}
	return (ascii+3)/4 + other
}

type builder struct {
	parts map[string]map[string]*PromptItem // kind -> itemKey -> item
	turns int
}

func itemKey(tag, name string) string { return tag + "\x00" + name }

func (b *builder) add(kind, name, tag string, tokens int, text string) {
	if tokens <= 0 {
		return
	}
	m := b.parts[kind]
	if m == nil {
		m = map[string]*PromptItem{}
		b.parts[kind] = m
	}
	k := itemKey(tag, name)
	if it := m[k]; it != nil {
		it.Tokens += tokens
		if tag != "turn" {
			it.N++
		}
		it.appendText(text)
	} else {
		it := &PromptItem{Name: name, Tag: tag, Tokens: tokens}
		it.appendText(text)
		m[k] = it
	}
}

// appendText keeps t's content bounded: the first itemTextCap bytes are kept,
// later additions append until the cap, and Truncated is set when cut.
func (it *PromptItem) appendText(text string) {
	if text == "" || it.Truncated {
		return
	}
	room := itemTextCap - len(it.Text)
	if room <= 0 {
		it.Truncated = true
		return
	}
	if len(text) > room {
		it.Text += text[:room]
		it.Truncated = true
		return
	}
	// separate successive pieces (turns, multiple bash results)
	if it.Text != "" {
		it.Text += "\n"
	}
	it.Text += text
}

// Analyze reads a request as proto and estimates its prompt's parts.
func Analyze(proto protocol.Protocol, body []byte) Prompt {
	req, err := protocol.Parse(proto, body)
	if err != nil {
		return Prompt{}
	}
	return AnalyzeRequest(req)
}

// AnalyzeRequest estimates the prompt of an IR request.
func AnalyzeRequest(req *protocol.Request) Prompt {
	b := &builder{parts: map[string]map[string]*PromptItem{}}
	for _, s := range req.System {
		classifyText(b, s)
	}
	for _, t := range req.Tools {
		def := t.Name + " " + t.Description + " " + string(t.Schema)
		b.add(PartTools, t.Name, "", TokensOf(def), def)
	}
	for _, m := range req.Messages {
		userTurn := m.Role == "user" && !hasOnlyToolResults(m)
		if userTurn {
			b.turns++
		}
		for _, p := range m.Parts {
			switch p.Kind {
			case protocol.Text:
				if m.Role == "system" || m.Role == "developer" {
					classifyText(b, p.Text)
				} else {
					b.add(PartChat, m.Role, "turn", TokensOf(p.Text), p.Text)
				}
			case protocol.Thinking:
				b.add(PartChat, "thinking", "turn", TokensOf(p.Text), p.Text)
			case protocol.Image:
				classifyImage(b)
			case protocol.ToolCall:
				name := p.Name
				b.add(PartChat, name, "call", TokensOf(string(p.Args)), string(p.Args))
			case protocol.ToolResult:
				name := p.Name
				if name == "" {
					name = resultNameFor(req, p.CallID)
				}
				kind, tag := PartResults, "shell"
				if readsFile(name) {
					kind, tag = PartFiles, "read"
				}
				b.add(kind, name, tag, TokensOf(p.Text), p.Text)
			}
		}
	}
	return b.finish()
}

// classifyText adds a system-like text: instruction files and reminders are
// memory, the rest is the agent's system prompt.
func classifyText(b *builder, text string) {
	lower := strings.ToLower(text)
	switch {
	case strings.Contains(lower, "agents.md"), strings.Contains(lower, "claude.md"),
		strings.Contains(lower, "<project_instructions"), strings.Contains(lower, "memory"),
		strings.Contains(lower, "<reminder"):
		names := instructionNames(text)
		if len(names) == 0 {
			b.add(PartMemory, "instructions", "memory", TokensOf(text), text)
			return
		}
		per := TokensOf(text) / len(names)
		for _, n := range names {
			b.add(PartMemory, n, "memory", per, text)
		}
	default:
		b.add(PartSystem, harnessName(text), "", TokensOf(text), text)
	}
}

func classifyImage(b *builder) {
	b.add(PartFiles, "image", "image", imageTokens, "")
}

func (b *builder) finish() Prompt {
	var parts []PromptPart
	total := 0
	for _, kind := range Order {
		m := b.parts[kind]
		if len(m) == 0 {
			continue
		}
		items := make([]PromptItem, 0, len(m))
		sum := 0
		for _, it := range m {
			items = append(items, *it)
			sum += it.Tokens
		}
		sort.Slice(items, func(i, j int) bool {
			if items[i].Tokens != items[j].Tokens {
				return items[i].Tokens > items[j].Tokens
			}
			return items[i].Name < items[j].Name
		})
		if len(items) > maxItems {
			rest := 0
			for _, it := range items[maxItems:] {
				rest += it.Tokens
			}
			items = append(items[:maxItems], PromptItem{Name: "另外", Tag: "more", N: len(items) - maxItems, Tokens: rest})
		}
		parts = append(parts, PromptPart{Kind: kind, Tokens: sum, Items: items})
		total += sum
	}
	return Prompt{Parts: parts, Tokens: total, Turns: b.turns}
}

func hasOnlyToolResults(m protocol.Message) bool {
	has := false
	for _, p := range m.Parts {
		if p.Kind == protocol.Text || p.Kind == protocol.Image {
			return false
		}
		if p.Kind == protocol.ToolResult {
			has = true
		}
	}
	return has
}

func resultNameFor(req *protocol.Request, callID string) string {
	for _, m := range req.Messages {
		for _, p := range m.Parts {
			if p.Kind == protocol.ToolCall && p.ID == callID {
				return p.Name
			}
		}
	}
	return "tool"
}

// readsFile reports whether a tool's name suggests it reads files into context.
func readsFile(name string) bool {
	l := strings.ToLower(name)
	for _, k := range []string{"read", "view", "open", "cat", "context7", "grep", "glob"} {
		if strings.Contains(l, k) {
			return true
		}
	}
	return false
}

// instructionNames pulls the file names out of an instruction block.
func instructionNames(text string) []string {
	var out []string
	for _, tok := range strings.FieldsFunc(text, func(r rune) bool {
		return !(r == '.' || r == '_' || r == '-' || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z') || (r >= '0' && r <= '9'))
	}) {
		l := strings.ToLower(tok)
		if strings.HasSuffix(l, ".md") || strings.HasSuffix(l, ".mdc") {
			if !containsStr(out, tok) {
				out = append(out, tok)
			}
		}
	}
	return out
}

// harnessName names a system message by the harness that wrote it.
func harnessName(text string) string {
	l := strings.ToLower(text)
	switch {
	case strings.Contains(l, "claude"):
		return "system prompt"
	case strings.Contains(l, "codex"), strings.Contains(l, "openai"):
		return "system prompt"
	case strings.Contains(l, "gemini"):
		return "system prompt"
	}
	return "system prompt"
}

func containsStr(s []string, v string) bool {
	for _, x := range s {
		if x == v {
			return true
		}
	}
	return false
}

// Usage is a vendor's token accounting, as a request's answer reported it.
type Usage struct {
	Input      int
	Output     int
	CacheRead  int
	CacheWrite int
	Known      bool // a vendor that counts no cache leaves this false
}

// CacheHit is the share of the prompt the vendor read from its cache, 0..1.
func (u Usage) CacheHit() float64 {
	total := u.Input + u.CacheRead + u.CacheWrite
	if total <= 0 {
		return 0
	}
	return float64(u.CacheRead) / float64(total)
}

// Calibrate scales a prompt's estimated parts to a vendor's real total and
// marks it counted.
func (p *Prompt) Calibrate(real int) {
	if real <= 0 || p.Tokens <= 0 || real == p.Tokens {
		p.Tokens = real
		p.Counted = true
		return
	}
	r := float64(real) / float64(p.Tokens)
	for i := range p.Parts {
		p.Parts[i].Tokens = int(float64(p.Parts[i].Tokens) * r)
		for j := range p.Parts[i].Items {
			p.Parts[i].Items[j].Tokens = int(float64(p.Parts[i].Items[j].Tokens) * r)
		}
	}
	p.Tokens = real
	p.Counted = true
}
