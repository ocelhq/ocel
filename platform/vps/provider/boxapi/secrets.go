package boxapi

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
		return seal.AssociatedData{}, refusal.Refuse(refusal.CodeInvalid, "the %s secret names no project, and its project is what the secret is sealed to", name)
	}
	if stack == "" {
		return seal.AssociatedData{}, refusal.Refuse(refusal.CodeInvalid, "the %s secret names no stack, and its stack is what the secret is sealed to", name)
	}
	return seal.BindToProject(project, "resource secret", string(tier), stack, folder, binding, name), nil
}

const (
	StoreSecretFolder  = "resources"
	StoreSecretBinding = "store"
	StoreSecretName    = "storekey"
)
