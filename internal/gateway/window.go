package gateway

import "strings"

// ContextWindow returns an approximate context window for a model id. A real
// gateway reads this from a catalog (models.dev); simple-magpie keeps a small
// table by name fragment and a sane default.
func ContextWindow(model string) int {
	m := strings.ToLower(model)
	switch {
	case strings.Contains(m, "claude"):
		return 200_000
	case strings.Contains(m, "gpt-4.1"), strings.Contains(m, "gpt-5"):
		return 1_000_000
	case strings.Contains(m, "gpt-4o"), strings.Contains(m, "o1"), strings.Contains(m, "o3"), strings.Contains(m, "o4"):
		return 128_000
	case strings.Contains(m, "gemini"):
		return 1_000_000
	case strings.Contains(m, "deepseek"):
		return 1_048_576
	case strings.Contains(m, "kimi"), strings.Contains(m, "moonshot"):
		return 256_000
	case strings.Contains(m, "glm"), strings.Contains(m, "zhipu"):
		return 200_000
	case strings.Contains(m, "qwen"):
		return 131_072
	case strings.Contains(m, "llama"), strings.Contains(m, "mistral"):
		return 128_000
	}
	return 128_000
}
