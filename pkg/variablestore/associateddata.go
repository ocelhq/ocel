package variablestore

import (
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
)

func newCellAssociatedData(scope Scope, at Coordinate) (seal.AssociatedData, error) {
	at = at.canonical()
	return newAssociatedData(scope, at.Environment, at.Folder, "", at.Key)
}

func newBindingAssociatedData(scope Scope, environment, name string) (seal.AssociatedData, error) {
	return newAssociatedData(scope, canonicalEnvironment(environment), rootFolder, name, bindingValueKey)
}

func newAssociatedData(scope Scope, environment, folder, binding, key string) (seal.AssociatedData, error) {
	if scope.Project == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid, "a value names no project, and its project is what the value is sealed to")
	}
	if key == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid, "a value names no key, and its key is what the value is sealed to")
	}
	return seal.AssociatedData{
		{Name: "project", Value: scope.Project},
		{Name: "class", Value: string(scope.Tier)},
		{Name: "environment", Value: environment},
		{Name: "folder", Value: folder},
		{Name: "binding", Value: binding},
		{Name: "key", Value: key},
	}, nil
}
