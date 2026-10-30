package orchestrator

import (
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestContextKeepsNewestMessagesWithinByteBudget(t *testing.T) {
	recent := []ContextMessage{{Speaker: "Old", Body: "oldest note that should be omitted"}}
	for i := range 12 {
		recent = append(recent, ContextMessage{Speaker: "Peer", Body: fmt.Sprintf("message-%02d %s", i, strings.Repeat("ä", 4000))})
	}
	text := BuildContext(ContextInput{Goal: "Current goal", Summary: strings.Repeat("old summary ", 2000), Recent: recent})
	if len(text) > groupContextBytes || !utf8.ValidString(text) {
		t.Fatalf("invalid bounded context: %d bytes", len(text))
	}
	if strings.Contains(text, "oldest note that should be omitted") || !strings.Contains(text, "message-11") || !strings.Contains(text, "Current goal") || !strings.HasSuffix(text, contextHandoff) {
		t.Fatal("lost context priority or routing guidance")
	}
	if !strings.Contains(text, contextOmission) {
		t.Fatal("truncation was not identified")
	}
}

func TestContextBoundsLargeMetadataAndSingleReply(t *testing.T) {
	for _, in := range []ContextInput{
		{},
		{Goal: strings.Repeat("👋", 100000), Members: []Member{{Name: "Yoda", Instructions: strings.Repeat("role", 100000)}}},
		{Recent: []ContextMessage{{Speaker: "Peer", Body: strings.Repeat("界", 100000) + "latest question?"}}},
		{Summary: strings.Repeat("summary", 100000)},
	} {
		text := BuildContext(in)
		if len(text) > groupContextBytes || !utf8.ValidString(text) || !strings.HasSuffix(text, contextHandoff) {
			t.Fatalf("invalid context: %d bytes", len(text))
		}
		if len(in.Recent) > 0 && !strings.Contains(text, "latest question?") {
			t.Fatal("oversized latest reply lost its question")
		}
	}
}
