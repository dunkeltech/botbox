package bots

import "regexp"

var openRouterModelID = regexp.MustCompile(`^~?[a-zA-Z0-9_][a-zA-Z0-9_.-]*/[a-zA-Z0-9_][a-zA-Z0-9_.:-]*$`)

// RunnerForProvider maps saved provider names and existing CLI aliases to a
// sandbox executable and the provider identifier used by the proxy.
func RunnerForProvider(provider string) (cli, upstream string) {
	switch provider {
	case "anthropic", "claude":
		return "claude", "anthropic"
	case "openai", "codex":
		return "codex", "openai"
	case "google", "agy":
		return "agy", "google"
	case "openrouter":
		return "pi", "openrouter"
	default:
		return "", ""
	}
}

// ValidOpenRouterModel checks for a provider/model slug, including variant suffixes.
func ValidOpenRouterModel(model string) bool {
	return len(model) <= 100 && openRouterModelID.MatchString(model)
}
