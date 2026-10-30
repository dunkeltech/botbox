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

// SplitInputDirective separates the question from the bot's final control line.
// A standalone or quoted line is ordinary message text.
func SplitInputDirective(content string) (body string, names []string, required bool) {
	trimmed := strings.TrimRight(content, "\r\n \t")
	n := strings.LastIndexByte(trimmed, '\n')
	if n < 0 {
		return content, nil, false
	}
	before, last := trimmed[:n], trimmed[n+1:]
	payload, found := strings.CutPrefix(last, inputDirective)
	if !found {
		payload, required = strings.CutPrefix(last, requiredInputDirective)
	}
	if (!found && !required) || strings.TrimSpace(before) == "" || len(payload) > 4096 {
		return content, nil, false
	}
	if json.Unmarshal([]byte(payload), &names) != nil || len(names) == 0 || len(names) > MaxInputRecipients {
		return content, nil, false
	}
	return strings.TrimRight(before, "\r\n \t"), names, required
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
