package provider

import (
	"context"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
)

type Bootstrap interface {
	Catalogue() []Feature

	Describe(ctx context.Context, tier environment.Tier) (BootstrapDescription, error)

	Plan(ctx context.Context, req BootstrapRequest) (Plan, error)

	Apply(ctx context.Context, req BootstrapRequest, progress progress.Log) error

	PlanRemove(ctx context.Context, tier environment.Tier) (Plan, error)

	Remove(ctx context.Context, tier environment.Tier, progress progress.Log) error
}

type BootstrapDescription struct {
	Tier    environment.Tier
	Present bool
	Stacks  []BootstrapStack

	Unfinished bool

	VendorState any
}

type BootstrapStack struct {
	Name    string
	Feature string
	Present bool

	DigestCurrent bool

	WrittenBy string
}

type Feature struct {
	Name      string
	Summary   string
	DependsOn []string
	Needs     []string
}

const (
	NeedsFrameworkPrefix = "framework:"
	NeedsEdgePrefix      = "edge:"
)

type BootstrapRequest struct {
	Tier environment.Tier

	Features []string

	Remove []string

	RefuseReplacements bool

	Repair bool

	WrittenBy WrittenBy

	VendorState any
}

const FeatureVarsKey = "vars-key"

func BootstrapCommand(tier environment.Tier) string {
	if tier == environment.TierPreview {
		return "ocel bootstrap preview"
	}
	return "ocel bootstrap production"
}

func BootstrapFeaturesCommand(tier environment.Tier) string {
	return BootstrapCommand(tier) + " --features"
}

func BootstrapVarsKeyCommand(tier environment.Tier) string {
	return BootstrapFeaturesCommand(tier) + " " + FeatureVarsKey
}
