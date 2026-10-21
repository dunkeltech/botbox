package notify

import "github.com/google/uuid"

const (
	notifyAll      = "all"
	notifyMentions = "mentions"
	notifyMute     = "mute"
	roleViewer     = "viewer"
)

// person is one human and the preference that decides which rows they get.
type person struct {
	ID     uuid.UUID
	Notify string
	Role   string
}

// mentionIDs keeps each named human once, unless they muted the group.
// notify=mentions still receives a mention. Unknown ids are not members.
func mentionIDs(mentioned []uuid.UUID, members []person) []uuid.UUID {
	prefs := make(map[uuid.UUID]string, len(members))
	for _, member := range members {
		prefs[member.ID] = member.Notify
	}
	seen := make(map[uuid.UUID]bool, len(mentioned))
	out := make([]uuid.UUID, 0, len(mentioned))
	for _, id := range mentioned {
		if id == uuid.Nil || seen[id] {
			continue
		}
		seen[id] = true
		pref, ok := prefs[id]
		if !ok || pref == notifyMute {
			continue
		}
		out = append(out, id)
	}
	return out
}

// approvalIDs is the set of humans who may decide, minus anyone who asked not
// to hear about approvals. A member with notify=mentions or mute is skipped
// even when they are an owner. An owner who never joined the group has no
// preference and is included.
func approvalIDs(members, deciders []person) []uuid.UUID {
	inGroup := make(map[uuid.UUID]bool, len(members))
	out := make([]uuid.UUID, 0, len(members)+len(deciders))
	seen := map[uuid.UUID]bool{}
	for _, member := range members {
		inGroup[member.ID] = true
		if member.ID == uuid.Nil || member.Role == roleViewer || member.Notify != notifyAll {
			continue
		}
		if seen[member.ID] {
			continue
		}
		seen[member.ID] = true
		out = append(out, member.ID)
	}
	for _, decider := range deciders {
		if decider.ID == uuid.Nil || inGroup[decider.ID] || seen[decider.ID] {
			continue
		}
		seen[decider.ID] = true
		out = append(out, decider.ID)
	}
	return out
}

// budgetIDs is every member who asked for all group notices. Viewers are
// included. mentions and mute are not. The warning is not an approval.
func budgetIDs(members []person) []uuid.UUID {
	seen := map[uuid.UUID]bool{}
	out := make([]uuid.UUID, 0, len(members))
	for _, member := range members {
		if member.ID == uuid.Nil || member.Notify != notifyAll || seen[member.ID] {
			continue
		}
		seen[member.ID] = true
		out = append(out, member.ID)
	}
	return out
}
