package envgate

import (
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type GroupState struct {
	Key     string
	Set     []string
	Missing []string
}

func GroupStates(definitions []*resourcesv1.VariableDefinition, groups []*resourcesv1.GroupDefinition, present []Cell, folder string) []GroupState {
	cells := make(heldCells, len(present))
	for _, cell := range present {
		cells[cell] = 0
	}

	out := make([]GroupState, 0, len(groups))
	for _, group := range groups {
		state := GroupState{Key: group.GetKey()}
		for _, definition := range definitions {
			if definition.GetGroup() != group.GetKey() {
				continue
			}
			switch {
			case resolves(definition, folder, cells):
				state.Set = append(state.Set, definition.GetKey())
			case needsValue(definition, definitions, groups, folder, cells):
				state.Missing = append(state.Missing, definition.GetKey())
			}
		}
		out = append(out, state)
	}
	return out
}

func needsValue(definition *resourcesv1.VariableDefinition, definitions []*resourcesv1.VariableDefinition, groups []*resourcesv1.GroupDefinition, binding string, held heldCells) bool {
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

type described interface {
	GetKey() string
	GetDescription() string
}

func groupDescription[T described](groups []T, key string) string {
	for _, group := range groups {
		if group.GetKey() == key {
			return group.GetDescription()
		}
	}
	return ""
}
