package variables

import (
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

type GroupCompleteness struct {
	Key     string
	Set     []string
	Missing []string
}

func ReadGroupCompleteness(definitions []*resourcesv1.VariableDefinition, groups []*resourcesv1.GroupDefinition, present []Cell, folder string) []GroupCompleteness {
	cells := make(presentCells, len(present))
	for _, cell := range present {
		cells[cell] = 0
	}

	out := make([]GroupCompleteness, 0, len(groups))
	for _, group := range groups {
		completeness := GroupCompleteness{Key: group.GetKey()}
		for _, definition := range definitions {
			if definition.GetGroup() != group.GetKey() {
				continue
			}
			switch {
			case resolves(definition, folder, cells):
				completeness.Set = append(completeness.Set, definition.GetKey())
			case needsValue(definition, definitions, groups, folder, cells):
				completeness.Missing = append(completeness.Missing, definition.GetKey())
			}
		}
		out = append(out, completeness)
	}
	return out
}

func needsValue(definition *resourcesv1.VariableDefinition, definitions []*resourcesv1.VariableDefinition, groups []*resourcesv1.GroupDefinition, binding string, present presentCells) bool {
	if !definition.GetRequired() {
		return false
	}
	group := definition.GetGroup()
	if group == "" || groupRequired(groups, group) {
		return true
	}
	return groupPresent(definitions, present, group, binding)
}

func resolves(definition *resourcesv1.VariableDefinition, binding string, present presentCells) bool {
	_, ok := resolveCell(definition, binding, present)
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

func groupPresent(definitions []*resourcesv1.VariableDefinition, present presentCells, group, binding string) bool {
	for _, definition := range definitions {
		if definition.GetGroup() != group {
			continue
		}
		if resolves(definition, binding, present) {
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
