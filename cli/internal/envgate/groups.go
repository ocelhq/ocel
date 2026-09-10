package envgate

import (
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type GroupStanding struct {
	Key     string
	Set     []string
	Missing []string
}

func Standings(definitions []*resourcesv1.VariableDefinition, groups []*resourcesv1.GroupDefinition, held []Cell, folder string) []GroupStanding {
	cells := make(heldCells, len(held))
	for _, cell := range held {
		cells[cell] = 0
	}

	out := make([]GroupStanding, 0, len(groups))
	for _, group := range groups {
		standing := GroupStanding{Key: group.GetKey()}
		for _, definition := range definitions {
			if definition.GetGroup() != group.GetKey() {
				continue
			}
			switch {
			case resolves(definition, folder, cells):
				standing.Set = append(standing.Set, definition.GetKey())
			case owes(definition, definitions, groups, folder, cells):
				standing.Missing = append(standing.Missing, definition.GetKey())
			}
		}
		out = append(out, standing)
	}
	return out
}

func owes(definition *resourcesv1.VariableDefinition, definitions []*resourcesv1.VariableDefinition, groups []*resourcesv1.GroupDefinition, binding string, held heldCells) bool {
	if !definition.GetRequired() {
		return false
	}
	group := definition.GetGroup()
	if group == "" || groupRequired(groups, group) {
		return true
	}
	return groupPresent(definitions, held, group, binding)
}

func resolves(definition *resourcesv1.VariableDefinition, binding string, held heldCells) bool {
	_, ok := hop(definition, binding, held)
	return ok
}

func groupRequired(groups []*resourcesv1.GroupDefinition, key string) bool {
	for _, group := range groups {
		if group.GetKey() == key {
			return group.GetRequired()
		}
	}
	return false
}

func groupPresent(definitions []*resourcesv1.VariableDefinition, held heldCells, group, binding string) bool {
	for _, definition := range definitions {
		if definition.GetGroup() != group {
			continue
		}
		if resolves(definition, binding, held) {
			return true
		}
	}
	return false
}

func groupDescription(groups []*resourcesv1.GroupDefinition, key string) string {
	for _, group := range groups {
		if group.GetKey() == key {
			return group.GetDescription()
		}
	}
	return ""
}
