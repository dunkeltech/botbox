// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"encoding/json"
	"strings"

	"github.com/google/uuid"
)

// MaxInputRecipients bounds one fan-out and leaves prompt space for every reply.
const MaxInputRecipients = 8

const inputDirective = "Request input: "
const requiredInputDirective = "Request required input: "
const humanHandoffDirective = "Handoff to human: "

func splitTrailingLine(content, prefix string) (before, payload string, ok bool) {
	trimmed := strings.TrimRight(content, "\r\n \t")
	n := strings.LastIndexByte(trimmed, '\n')
	if n < 0 {
		return content, "", false
	}
	before, last := trimmed[:n], trimmed[n+1:]
	payload, found := strings.CutPrefix(last, prefix)
	if !found || strings.TrimSpace(before) == "" || len(payload) > 4096 {
		return content, "", false
	}
	return strings.TrimRight(before, "\r\n \t"), payload, true
}

// SplitInputDirective separates the question from the bot's final control line.
// A standalone or quoted line is ordinary message text.
func SplitInputDirective(content string) (body string, names []string, required bool) {
	before, payload, ok := splitTrailingLine(content, inputDirective)
	if !ok {
		before, payload, required = splitTrailingLine(content, requiredInputDirective)
		if !required {
			return content, nil, false
		}
	}
	if json.Unmarshal([]byte(payload), &names) != nil || len(names) == 0 || len(names) > MaxInputRecipients {
		return content, nil, false
	}
	return before, names, required
}

// SplitHumanHandoffDirective separates the message body from a final
// `Handoff to human: ["<uuid>", ...]` control line. Invalid payloads are kept
// as ordinary message text.
func SplitHumanHandoffDirective(content string) (body string, userIDs []uuid.UUID) {
	before, payload, ok := splitTrailingLine(content, humanHandoffDirective)
	if !ok {
		return content, nil
	}
	var raw []string
	if json.Unmarshal([]byte(payload), &raw) != nil || len(raw) == 0 || len(raw) > MaxInputRecipients {
		return content, nil
	}
	seen := make(map[uuid.UUID]bool, len(raw))
	ids := make([]uuid.UUID, 0, len(raw))
	for _, s := range raw {
		id, err := uuid.Parse(strings.TrimSpace(s))
		if err != nil || id == uuid.Nil {
			return content, nil
		}
		if !seen[id] {
			seen[id] = true
			ids = append(ids, id)
		}
	}
	return before, ids
}

// ExtractControlDirectives strips trailing bot-to-bot input and human handoff
// control lines in any order and returns their parsed targets.
func ExtractControlDirectives(content string) (body string, inputNames []string, required bool, handoffUserIDs []uuid.UUID) {
	cur := content
	for range 2 {
		if handoffUserIDs == nil {
			if next, ids := SplitHumanHandoffDirective(cur); len(ids) > 0 {
				cur = next
				handoffUserIDs = ids
				continue
			}
		}
		if inputNames == nil {
			if next, names, req := SplitInputDirective(cur); len(names) > 0 {
				cur = next
				inputNames = names
				required = req
				continue
			}
		}
		break
	}
	return cur, inputNames, required, handoffUserIDs
}

func inputRecipients(req Request, names []string) []uuid.UUID {
	if len(names) == 1 && strings.EqualFold(names[0], "@all") {
		var ids []uuid.UUID
		for _, member := range req.Members {
			if member.Kind == MemberBot && member.ID != req.SenderID {
				ids = append(ids, member.ID)
			}
		}
		if len(ids) > MaxInputRecipients {
			return nil
		}
		return ids
	}
	var ids []uuid.UUID
	for _, name := range names {
		var id uuid.UUID
		for _, member := range req.Members {
			if member.Kind == MemberBot && strings.EqualFold(name, member.Name) {
				if id != uuid.Nil {
					return nil
				}
				id = member.ID
			}
		}
		if id == uuid.Nil {
			return nil
		}
		ids = append(ids, id)
	}
	return withoutSelf(req, ids)
}
