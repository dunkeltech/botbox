package api

import (
	"encoding/base64"
	"encoding/json"
	"strings"

	"github.com/dunkeltech/botbox/backend/internal/desk"
)

// Computer tool profiles select the provider's native computer-use shape.
const (
	ComputerAnthropic = "anthropic"
	ComputerOpenAI    = "openai"
	ComputerGeneric   = "generic"
)

// ComputerToolVersion is the current Anthropic computer-use tool and beta header.
const (
	ComputerToolVersion = "computer_20251124"
	ComputerBetaHeader  = "computer-use-2025-11-24"
)

type imageEnvelope struct {
	BotboxImage string `json:"botboxImage"`
	Mime        string `json:"mime"`
	Text        string `json:"text"`
	Width       int    `json:"width"`
	Height      int    `json:"height"`
}

// EncodeImageResult packs a screenshot for a tool result without putting the
// bytes into a log line. Provider adapters expand it into image content.
func EncodeImageResult(text, mime string, image []byte, width, height int) string {
	raw, err := json.Marshal(imageEnvelope{BotboxImage: base64.StdEncoding.EncodeToString(image), Mime: mime, Text: text, Width: width, Height: height})
	if err != nil {
		return text
	}
	return string(raw)
}

func decodeImageResult(raw string) (imageEnvelope, bool) {
	if !strings.Contains(raw, "botboxImage") {
		return imageEnvelope{}, false
	}
	var env imageEnvelope
	if json.Unmarshal([]byte(raw), &env) != nil || env.BotboxImage == "" {
		return imageEnvelope{}, false
	}
	if env.Mime == "" {
		env.Mime = "image/png"
	}
	return env, true
}

func computerTools(profile string) []any {
	switch profile {
	case ComputerAnthropic:
		schema, _ := json.Marshal(map[string]any{"type": "object", "x-botbox-computer": "anthropic", "display_width_px": desk.Width, "display_height_px": desk.Height, "version": ComputerToolVersion, "beta": ComputerBetaHeader})
		return []any{map[string]any{"type": "function", "function": map[string]any{"name": "computer", "description": "Drive the 1280x800 desktop. Prefer browser tools and shell commands for web pages.", "parameters": json.RawMessage(schema)}}}
	case ComputerOpenAI:
		schema, _ := json.Marshal(map[string]any{"type": "object", "x-botbox-computer": "openai", "display_width": desk.Width, "display_height": desk.Height, "environment": "linux"})
		return []any{map[string]any{"type": "function", "function": map[string]any{"name": "computer", "description": "Drive the linux desktop.", "parameters": json.RawMessage(schema)}}}
	case ComputerGeneric:
		tools := []any{}
		for _, spec := range []struct{ name, desc string }{
			{"screenshot", "Capture the desktop. Returns an image."},
			{"click", "Click x, y. Include image_width and image_height when the screenshot was scaled."},
			{"type", "Type text into the focused window."},
			{"key", "Press a key chord."},
			{"mouse_move", "Move the pointer."},
			{"scroll", "Scroll the desktop."},
			{"browser_navigate", "Open an http(s) URL in the sandbox browser."},
			{"browser_click", "Click a CSS selector."},
			{"browser_fill", "Fill a CSS selector."},
			{"browser_read", "Read text from a CSS selector."},
		} {
			tools = append(tools, map[string]any{"type": "function", "function": map[string]any{"name": spec.name, "description": spec.desc + " " + "Prefer shell and browser tools over pixel clicks.", "parameters": map[string]any{"type": "object"}}})
		}
		return tools
	default:
		return nil
	}
}

func screenshotCall(name, args string) bool {
	if name == "screenshot" || name == "browser_screenshot" {
		return true
	}
	if name != "computer" {
		return false
	}
	var body struct {
		Action string `json:"action"`
		Type   string `json:"type"`
	}
	if json.Unmarshal([]byte(args), &body) != nil {
		return false
	}
	return body.Action == "screenshot" || body.Type == "screenshot"
}
