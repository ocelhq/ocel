package provider

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/refusal"
)

func RefuseUnconsentedChanges(shown, fresh Plan) error {
	rows := map[string]ChangeAction{}
	for _, group := range shown.Groups {
		rows[group.Name] = group.Action
		for _, change := range group.Changes {
			rows[rowKey(group, change)] = change.Action
		}
	}

	var drift unconsentedDrift
	for _, group := range fresh.Groups {
		if len(group.Changes) == 0 {
			drift.note(group.Name, rows[group.Name], group.Action)
			continue
		}
		for _, change := range group.Changes {
			drift.note(change.Name, rows[rowKey(group, change)], change.Action)
		}
	}
	return drift.refuse()
}

func rowKey(group ChangeGroup, change Change) string {
	return group.Name + "/" + change.Kind + "/" + change.Name
}

type unconsentedDrift struct {
	grown, destroying []string
}

func (d *unconsentedDrift) note(name string, shown, current ChangeAction) {
	switch {
	case !current.Writes():
	case shown == "" || !shown.Writes():
		d.grown = append(d.grown, name)
	case destroys(current) && current != shown:
		d.destroying = append(d.destroying, fmt.Sprintf("%s (shown as %s, now %s)", name, shown, current))
	}
}

func (d unconsentedDrift) refuse() error {
	var moved []string
	if len(d.grown) > 0 {
		moved = append(moved, strings.Join(d.grown, ", ")+" needed no change when the plan was drawn and now does")
	}
	if len(d.destroying) > 0 {
		moved = append(moved, strings.Join(d.destroying, ", ")+" would now be taken down in a way the plan never showed")
	}
	if len(moved) == 0 {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"%s, so this apply would do work nobody consented to.\n"+
			"Draw the plan again and consent to what it shows now",
		strings.Join(moved, "; "))
}

func destroys(action ChangeAction) bool {
	switch action {
	case ActionDelete, ActionReplace, ActionDisableThenDelete:
		return true
	default:
		return false
	}
}
