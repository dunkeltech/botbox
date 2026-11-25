// SPDX-License-Identifier: AGPL-3.0-only

package llmproxy

import "encoding/json"

func nativeMCPDeclarations(raw []byte, tools map[string]func(json.RawMessage) error) (json.RawMessage, bool) {
	if len(raw) == 0 {
		return nil, true
	}
	var groups []struct {
		Functions []json.RawMessage `json:"functionDeclarations"`
	}
	if json.Unmarshal(raw, &groups) != nil {
		return nil, false
	}
	var declared []json.RawMessage
	seen := make(map[string]bool)
	for _, group := range groups {
		for _, raw := range group.Functions {
			var function struct {
				Name string `json:"name"`
			}
			if !decodeJSONObject(raw, &function) {
				return nil, false
			}
			if tools[function.Name] == nil {
				continue
			}
			if seen[function.Name] {
				return nil, false
			}
			seen[function.Name] = true
			declared = append(declared, raw)
		}
	}
	if len(declared) == 0 {
		return nil, true
	}
	// Only work sandbox tools reach the model. Native file, shell, and web
	// capabilities stay out of the credential-bearing runtime.
	safe, err := json.Marshal([]map[string][]json.RawMessage{{"functionDeclarations": declared}})
	return safe, err == nil
}

func validNativeMCPCall(value any, tools map[string]func(json.RawMessage) error) bool {
	call, ok := value.(map[string]any)
	if !ok {
		return false
	}
	for key := range call {
		if key != "name" && key != "id" && key != "args" {
			return false
		}
	}
	if id, present := call["id"]; present {
		text, ok := id.(string)
		if !ok || len(text) > 256 {
			return false
		}
	}
	name, _ := call["name"].(string)
	validate := tools[name]
	if validate == nil {
		return false
	}
	var payload json.RawMessage
	if args, present := call["args"]; present {
		data, err := json.Marshal(args)
		if err != nil {
			return false
		}
		payload = data
	}
	return validate(payload) == nil
}
