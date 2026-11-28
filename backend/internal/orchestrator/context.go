// SPDX-License-Identifier: AGPL-3.0-only

package orchestrator

import (
	"fmt"
	"strings"
	"unicode/utf8"
)

// Group context must leave room for the complete current message, the bot's
// identity and desktop guidance within the agent's 64 KiB CLI input contract.
const groupContextBytes = 48 * 1024

const contextOmission = "[Earlier context omitted to fit the input limit.]\n"
const contextHandoff = "When bot_talk is true and you need input from other bots, write the actual question in your reply, then put a control line at the very end: Request input: [\"Exact Bot Name\", \"Another Bot Name\"]. Use Request required input: with the same list if missing input must block the task. Use up to eight other bots. The list [\"@all\"] requests input from all other bots if they fit within the limit. Chat mentions are references. After requesting input, end your turn. The group will bring replies back together. If you are answering an input request, give your own answer and finish that turn. Once you receive the collected replies, synthesize them for the human. Request more input only for a concrete unresolved question. Check human members' roles in the roster when a task needs human review or action. To assign work or request a review from a human member of this group, @mention them by their exact roster name with a clear request and end your turn so they can respond.\n"

func boundedContext(header, summary string, recent []ContextMessage) string {
	// Very large rosters must not consume the entire context. The responding
	// bot's full responsibility is also sent separately with its identity.
	omitted := len(header) > groupContextBytes/2
	header = contextPrefix(header, groupContextBytes/2)
	remaining := groupContextBytes - len(header) - len(contextHandoff) - len(contextOmission) - len("Recent messages:\n")
	var selected []string
	for i := len(recent) - 1; i >= 0; i-- {
		line := fmt.Sprintf("%s: %s\n", recent[i].Speaker, recent[i].Body)
		if len(line) > remaining {
			omitted = true
			if len(selected) == 0 {
				speaker := contextPrefix(recent[i].Speaker+": ", remaining-1)
				selected = append(selected, speaker+contextTail(recent[i].Body, remaining-len(speaker)-1)+"\n")
				remaining = 0
			}
			break
		}
		selected = append(selected, line)
		remaining -= len(line)
	}
	var older string
	if strings.TrimSpace(summary) != "" {
		const label = "Earlier summary:\n"
		if remaining > len(label)+1 {
			text := contextPrefix(summary, remaining-len(label)-1)
			older = label + text + "\n"
			omitted = omitted || len(text) < len(summary)
		} else {
			omitted = true
		}
	}
	var out strings.Builder
	out.WriteString(header)
	if omitted {
		out.WriteString(contextOmission)
	}
	out.WriteString(older)
	if len(selected) > 0 {
		out.WriteString("Recent messages:\n")
		for i := len(selected) - 1; i >= 0; i-- {
			out.WriteString(selected[i])
		}
	}
	out.WriteString(contextHandoff)
	return out.String()
}

func contextPrefix(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	for limit > 0 && !utf8.RuneStart(text[limit]) {
		limit--
	}
	return text[:limit]
}

func contextTail(text string, limit int) string {
	if limit <= 0 {
		return ""
	}
	if len(text) <= limit {
		return text
	}
	start := len(text) - limit
	for start < len(text) && !utf8.RuneStart(text[start]) {
		start++
	}
	return text[start:]
}
