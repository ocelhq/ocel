package provider

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/progress"
)

type Bootstrap interface {
	Parts() []Part

	Describe(ctx context.Context, tier environment.Tier) (BootstrapDescription, error)

	Plan(ctx context.Context, req BootstrapRequest) (Plan, error)

	Apply(ctx context.Context, req BootstrapRequest, progress progress.Log) error

	PlanRemove(ctx context.Context, tier environment.Tier) (Plan, error)

	Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error

	DescribeParts(ctx context.Context, described BootstrapDescription) ([]BootstrapPart, error)
}

type BootstrapDescription struct {
	Tier    environment.Tier
	Present bool
	Stacks  []BootstrapStack

	Unfinished bool

	Identity *TierIdentity

	Trust *WebIdentityTrust
}

type BootstrapStack struct {
	Name    string
	Part    string
	Present bool

	DigestCurrent bool

	WrittenBy string

	ReadError string
}

const BasePart = "base"

type BootstrapPart struct {
	Name                string
	Summary             string
	Current             bool
	Digest              string
	Resources           []PhysicalResource
	UsedBy              []string
	UsedByEveryProject  bool
	RetiresWithLastUser bool
	Inventory           *PartInventory
}

type PartInventory struct {
	Vendor    string
	Region    string
	Resources []pricing.Resource
}

type Part struct {
	Name    string
	Summary string
	Edges   []edge.Kind
}

type BootstrapRequest struct {
	Tier environment.Tier

	Parts []string

	Remove []string

	Force bool

	RefuseReplacements bool

	WrittenBy WrittenBy

	Trust *WebIdentityTrust
}

type WantedPartError struct {
	Part string
	Err  error
}

func (e WantedPartError) Error() string { return e.Err.Error() }

func (e WantedPartError) Unwrap() error { return e.Err }

const VariablesKeyPart = "variables-key"

func BootstrapCommand(tier environment.Tier) string {
	if tier == environment.TierPreview {
		return "ocel bootstrap preview"
	}
	return "ocel bootstrap production"
}
