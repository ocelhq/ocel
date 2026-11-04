package variablestore

import (
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
)

const (
	variablePurpose = "variable"
	bindingPurpose  = "binding"
)

func newCellAssociatedData(scope Scope, at Coordinate) (seal.AssociatedData, error) {
	at = at.canonical()
	if err := refuseUnsealable(scope, at.Key); err != nil {
		return seal.AssociatedData{}, err
	}
	return seal.BindToProject(scope.Project, variablePurpose, string(scope.Tier), at.Environment, at.Folder, at.Key), nil
}

func newBindingAssociatedData(scope Scope, environment, name string) (seal.AssociatedData, error) {
	if err := refuseUnsealable(scope, name); err != nil {
		return seal.AssociatedData{}, err
	}
	return seal.BindToProject(scope.Project, bindingPurpose, string(scope.Tier), canonicalEnvironment(environment), name), nil
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

func OwnerOfAssociatedData(bound seal.AssociatedData) (records.Owner, bool) {
	project, named := bound.Project()
	values := bound.Values()
	switch {
	case !named:
		return records.Owner{}, false
	case bound.Purpose() == variablePurpose && len(values) == 4:
		return ownerOfValue(project, values[1]), true
	case bound.Purpose() == bindingPurpose && len(values) == 3:
		return ownerOfValue(project, values[1]), true
	}
	return records.Owner{}, false
}
