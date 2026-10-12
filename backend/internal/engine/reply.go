package engine

import (
	"errors"
	"strings"
)

// replyText combines live deltas and completed message snapshots without
// duplicating providers that emit both representations of the same reply.
type replyText struct {
	strings.Builder
	deltas bool
}

func (r *replyText) Add(ev Event) error {
	if ev.Kind != EventKindTextDelta && ev.Kind != EventKindMessage {
		return nil
	}
	appendText := ev.Kind == EventKindTextDelta || !r.deltas
	if appendText {
		if r.Len()+len(ev.Text) > 1<<20 {
			return errors.New("turn output exceeds limit")
		}
		_, _ = r.WriteString(ev.Text)
	}
	r.deltas = ev.Kind == EventKindTextDelta
	return nil
}
