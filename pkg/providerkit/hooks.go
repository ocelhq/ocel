package providerkit

import (
	"context"
	"fmt"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

type Hooks struct {
	PreflightDeploy     func(ctx context.Context, pre DeployPreflight) error
	VerifyGrants        func(ctx context.Context, binding Binding) error
	InspectStack        func(ctx context.Context, ref StackRef) (StackState, error)
	PackApp             func(ctx context.Context, packing AppPacking, progress Progress) (AppPack, error)
	EmbedCode           func(ctx context.Context, function string, artifact ArtifactRef, progress Progress) error
	WarmFunctions       func(ctx context.Context, targets []string, progress Progress) error
	ProgramEdge         func(ctx context.Context, req EdgeProgramRequest) (EdgeProgram, error)
	EnsureImageRegistry func(ctx context.Context, class Class, repositories []string) (RegistryTarget, error)
	RegistryImages      func(ctx context.Context, target RegistryTarget) (ImageStore, error)
	DirectImages        func(ctx context.Context) (ImageStore, error)
	CheckHost           func(ctx context.Context, req StandingRequest) ([]StandingCheck, error)
	ShapeCost           func(ctx context.Context, req ShapeRequest) (*costv1.ResourceSet, error)
	EstimateCost        func(ctx context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error)
	FunctionBaseImage   func(ctx context.Context, runtime Framework) (v1.Image, error)
	FunctionRuntime     func(ctx context.Context, runtime Framework) ([]byte, error)
}

func (h Hooks) refuseHalfPairs() error {
	for _, pair := range []struct {
		first, second       string
		hasFirst, hasSecond bool
	}{
		{"ShapeCost", "EstimateCost", h.ShapeCost != nil, h.EstimateCost != nil},
		{"FunctionBaseImage", "FunctionRuntime", h.FunctionBaseImage != nil, h.FunctionRuntime != nil},
	} {
		if pair.hasFirst == pair.hasSecond {
			continue
		}
		set, unset := pair.first, pair.second
		if pair.hasSecond {
			set, unset = pair.second, pair.first
		}
		return fmt.Errorf("the provider's hooks set %s and leave %s nil, and one is never called without the other", set, unset)
	}
	return nil
}
