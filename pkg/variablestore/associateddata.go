package variablestore

import (
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
)

func newCellAssociatedData(scope Scope, at Coordinate) (seal.AssociatedData, error) {
	at = at.canonical()
	if err := refuseUnsealable(scope, at.Key); err != nil {
		return seal.AssociatedData{}, err
	}
	return seal.BindToProject(scope.Project, "variable", string(scope.Tier), at.Environment, at.Folder, at.Key), nil
}

func newBindingAssociatedData(scope Scope, environment, name string) (seal.AssociatedData, error) {
	if err := refuseUnsealable(scope, name); err != nil {
		return seal.AssociatedData{}, err
	}
	return seal.BindToProject(scope.Project, "binding", string(scope.Tier), canonicalEnvironment(environment), name), nil
}

func refuseUnsealable(scope Scope, name string) error {
	if scope.Project == "" {
		return refusal.Refuse(refusal.CodeInvalid, "a value names no project, and its project is what the value is sealed to")
	}
	if name == "" {
		return refusal.Refuse(refusal.CodeInvalid, "a value names no key, and its key is what the value is sealed to")
	}
	return nil
}
