package envvars

import "github.com/ocelhq/ocel/pkg/seal"

func cellAssociatedData(scope Scope, at Coordinate) seal.AssociatedData {
	at = at.canonical()
	return associatedData(scope, at.Environment, at.Folder, "", at.Key)
}

func bindingAssociatedData(scope Scope, environment, name string) seal.AssociatedData {
	return associatedData(scope, canonicalEnvironment(environment), rootFolder, name, bindingValueKey)
}

func associatedData(scope Scope, environment, folder, binding, key string) seal.AssociatedData {
	return seal.AssociatedData{
		{Name: "project", Value: scope.Project},
		{Name: "class", Value: string(scope.Tier)},
		{Name: "environment", Value: environment},
		{Name: "folder", Value: folder},
		{Name: "binding", Value: binding},
		{Name: "key", Value: key},
	}
}
