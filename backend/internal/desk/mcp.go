// SPDX-License-Identifier: AGPL-3.0-only

package desk

// MCPTools describes the desktop controls shared by the sandbox and ACP clients.
func MCPTools() []map[string]any {
	point := map[string]any{
		"x":            map[string]any{"type": "integer"},
		"y":            map[string]any{"type": "integer"},
		"image_width":  map[string]any{"type": "integer"},
		"image_height": map[string]any{"type": "integer"},
	}
	return []map[string]any{
		{"name": "screenshot", "description": "Capture the 1280x800 desktop. Optional format png or jpeg and max_width. Coordinates on a downscaled shot are scaled back to the desktop.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"format": map[string]any{"type": "string", "enum": []string{"png", "jpeg"}}, "max_width": map[string]any{"type": "integer"}, "quality": map[string]any{"type": "integer"}}}},
		{"name": "cursor_position", "description": "Read the pointer position on the 1280x800 desktop.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{}}},
		{"name": "mouse_move", "description": "Move the pointer. Prefer shell or browser tools when a page has selectors.", "inputSchema": map[string]any{"type": "object", "required": []string{"x", "y"}, "properties": point}},
		{"name": "click", "description": "Click a mouse button at x, y.", "inputSchema": map[string]any{"type": "object", "required": []string{"x", "y"}, "properties": merge(point, map[string]any{"button": map[string]any{"type": "string", "enum": []string{"left", "right", "middle", "double", "triple"}}})}},
		{"name": "drag", "description": "Drag the left button from x, y to to_x, to_y.", "inputSchema": map[string]any{"type": "object", "required": []string{"x", "y", "to_x", "to_y"}, "properties": merge(point, map[string]any{"to_x": map[string]any{"type": "integer"}, "to_y": map[string]any{"type": "integer"}})}},
		{"name": "scroll", "description": "Scroll by dx and dy ticks, at most 20. Positive dy scrolls down.", "inputSchema": map[string]any{"type": "object", "properties": merge(point, map[string]any{"dx": map[string]any{"type": "integer"}, "dy": map[string]any{"type": "integer"}})}},
		{"name": "type", "description": "Type text into the focused window with a short per-character delay.", "inputSchema": map[string]any{"type": "object", "required": []string{"text"}, "properties": map[string]any{"text": map[string]any{"type": "string"}}}},
		{"name": "key", "description": "Press a key or chord such as Return or ctrl+l.", "inputSchema": map[string]any{"type": "object", "required": []string{"key"}, "properties": map[string]any{"key": map[string]any{"type": "string"}}}},
		{"name": "wait", "description": "Wait up to 10 seconds. A human takeover cancels the wait.", "inputSchema": map[string]any{"type": "object", "required": []string{"ms"}, "properties": map[string]any{"ms": map[string]any{"type": "integer"}}}},
		{"name": "browser_navigate", "description": "Open an http(s) URL in the sandbox browser. More reliable than pixel clicks on web pages.", "inputSchema": map[string]any{"type": "object", "required": []string{"url"}, "properties": map[string]any{"url": map[string]any{"type": "string"}}}},
		{"name": "browser_click", "description": "Click a CSS selector in the sandbox browser.", "inputSchema": map[string]any{"type": "object", "required": []string{"selector"}, "properties": map[string]any{"selector": map[string]any{"type": "string"}}}},
		{"name": "browser_fill", "description": "Fill a form control selected by CSS selector.", "inputSchema": map[string]any{"type": "object", "required": []string{"selector", "text"}, "properties": map[string]any{"selector": map[string]any{"type": "string"}, "text": map[string]any{"type": "string"}}}},
		{"name": "browser_read", "description": "Read visible text from a CSS selector, or the page when selector is omitted.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"selector": map[string]any{"type": "string"}}}},
		{"name": "browser_screenshot", "description": "Capture a browser element, or the viewport when selector is omitted.", "inputSchema": map[string]any{"type": "object", "properties": map[string]any{"selector": map[string]any{"type": "string"}}}},
	}
}

func merge(base, extra map[string]any) map[string]any {
	out := map[string]any{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}
