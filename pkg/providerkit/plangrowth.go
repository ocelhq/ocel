package providerkit

import "strings"

func RefuseGrowth(shown, standing Plan) error {
	rows := map[string]ChangeAction{}
	for _, group := range shown.Groups {
		rows[group.Name] = group.Action
		for _, change := range group.Changes {
			rows[rowKey(group, change)] = change.Action
		}
	}

	var grown []string
	for _, group := range standing.Groups {
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
	return Refuse(CodeInvalid,
		"%s stood as the plan was drawn and no longer does, so this apply would do work nobody consented to.\n"+
			"Draw the plan again and consent to what it shows now",
		strings.Join(grown, ", "))
}

func rowKey(group ChangeGroup, change Change) string {
	return group.Name + "/" + change.Kind + "/" + change.Name
}

func appendGrown(grown []string, name string, shown, standing ChangeAction) []string {
	if standing == ActionKeep || (shown != "" && shown != ActionKeep) {
		return grown
	}
	return append(grown, name)
}
