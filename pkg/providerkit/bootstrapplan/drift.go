package bootstrapplan

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
)

func RefuseUnconsentedChanges(shown, fresh provider.Plan) error {
	rows := map[string]provider.ChangeAction{}
	for _, group := range shown.Groups {
		rows[group.Name] = group.Action
		for _, change := range group.Changes {
			rows[rowKey(group, change)] = change.Action
		}
	}

	var grown []string
	for _, group := range fresh.Groups {
		if len(group.Changes) == 0 {
			grown = appendGrown(grown, group.Name, rows[group.Name], group.Action)
			continue
		}
		for _, change := range group.Changes {
			grown = appendGrown(grown, change.Name, rows[rowKey(group, change)], change.Action)
		}
	}
	if len(grown) == 0 {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"%s stood as the plan was drawn and no longer does, so this apply would do work nobody consented to.\n"+
			"Draw the plan again and consent to what it shows now",
		strings.Join(grown, ", "))
}

func rowKey(group provider.ChangeGroup, change provider.Change) string {
	return group.Name + "/" + change.Kind + "/" + change.Name
}

func appendGrown(grown []string, name string, shown, standing provider.ChangeAction) []string {
	if !standing.Writes() || (shown != "" && shown.Writes()) {
		return grown
	}
	return append(grown, name)
}
