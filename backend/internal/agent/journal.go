package agent

import (
	"encoding/json"
	"sync"
	"time"
)

// Journal keeps desktop actions until the API persists them.
// Screenshot bytes stay here, not in process logs, and leave memory on ack.
type Journal struct {
	mu      sync.Mutex
	session string
	next    uint64
	items   []JournalEntry
	limit   int
}

// JournalEntry is one desktop action waiting to be stored with the workspace.
type JournalEntry struct {
	Seq       uint64
	Session   string
	Kind      string
	Params    json.RawMessage
	Result    string
	ErrorCode string
	Image     []byte
	Mime      string
	X, Y      int32
	HasPoint  bool
	At        time.Time
}

// NewJournal starts a fresh sequence for this agent process.
func NewJournal(session string) *Journal {
	if session == "" {
		session = "0"
	}
	return &Journal{session: session, limit: 200}
}

// Session is the agent lifetime id mixed into the database idempotency key.
func (j *Journal) Session() string {
	if j == nil {
		return ""
	}
	return j.session
}

// Record appends one action. Oldest unacked entries are dropped past the cap
// so a client that never lists the timeline cannot grow sandbox memory.
func (j *Journal) Record(entry JournalEntry) {
	if j == nil {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	j.next++
	entry.Seq = j.next
	entry.Session = j.session
	if entry.At.IsZero() {
		entry.At = time.Now().UTC()
	}
	if len(entry.Image) > 1_500_000 {
		entry.Image = nil
		entry.Mime = ""
	}
	j.items = append(j.items, entry)
	if len(j.items) > j.limit {
		j.items = append([]JournalEntry(nil), j.items[len(j.items)-j.limit:]...)
	}
}

// Pull returns up to twenty unacked actions.
func (j *Journal) Pull() []JournalEntry {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	n := len(j.items)
	if n > 20 {
		n = 20
	}
	out := make([]JournalEntry, n)
	copy(out, j.items[:n])
	return out
}

// Ack drops actions at or below seq for this session.
func (j *Journal) Ack(session string, through uint64) {
	if j == nil || session != j.session {
		return
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	kept := j.items[:0]
	for _, item := range j.items {
		if item.Seq > through {
			kept = append(kept, item)
		}
	}
	j.items = kept
}
