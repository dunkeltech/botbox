// SPDX-License-Identifier: AGPL-3.0-only

package desk

import (
	"encoding/json"
	"time"
)

// StoredAction is one desktop action waiting to be saved with the workspace.
// Params and Image can contain what the bot typed or saw. Callers must not
// write either value to a log line.
type StoredAction struct {
	Seq       uint64
	Session   string
	Kind      string
	Params    json.RawMessage
	Result    string
	ErrorCode string
	Image     []byte
	Mime      string
	X         int32
	Y         int32
	HasPoint  bool
	At        time.Time
}
