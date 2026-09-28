package live

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/seal"
)

func NewStoreSecretAssociatedData(project string, tier environment.Tier, stack string) (seal.AssociatedData, error) {
	return NewSecretAssociatedData(project, tier, stack, StoreSecretFolder, StoreSecretBinding, StoreSecretName)
}

func NewSecretAssociatedData(project string, tier environment.Tier, stack, folder, binding, name string) (seal.AssociatedData, error) {
	if project == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid, "the %s secret names no project, and its project is what the secret is sealed to", name)
	}
	if stack == "" {
		return nil, refusal.Refuse(refusal.CodeInvalid, "the %s secret names no stack, and its stack is what the secret is sealed to", name)
	}
	return seal.AssociatedData{
		{Name: "project", Value: project},
		{Name: "tier", Value: string(tier)},
		{Name: "stack", Value: stack},
		{Name: "folder", Value: folder},
		{Name: "binding", Value: binding},
		{Name: "name", Value: name},
	}, nil
}
