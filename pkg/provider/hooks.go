package provider

import (
	"context"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/appbuild"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/progress"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

type Hooks struct {
	PreflightDeploy     func(ctx context.Context, pre DeployPreflight) error
	VerifyGrants        func(ctx context.Context, binding Binding) error
	InspectStack        func(ctx context.Context, ref StackRef) (InspectedStack, error)
	PackApp             func(ctx context.Context, req PackAppRequest, progress progress.Log) (PackAppResult, error)
	EmbedCode           func(ctx context.Context, function string, artifact ArtifactRef, progress progress.Log) error
	WarmFunctions       func(ctx context.Context, targets []string, progress progress.Log) error
	ProgramEdge         func(ctx context.Context, req EdgeProgramRequest) (EdgeProgram, error)
	EnsureImageRegistry func(ctx context.Context, tier environment.Tier) (RegistryTarget, error)
	OpenRegistryImages  func(ctx context.Context, target RegistryTarget) (ImageStore, error)
	OpenDirectImages    func(ctx context.Context) (ImageStore, error)
	CheckHost           func(ctx context.Context, req HostCheckRequest) ([]HostCheck, error)
	ProveIdentity       func(ctx context.Context, audience string) (envsource.IdentityProof, error)
	Cost                *CostHooks
	FunctionImages      *FunctionImageHooks
}

type CostHooks struct {
	Shape    func(ctx context.Context, req ShapeRequest) (*costv1.ResourceSet, error)
	Estimate func(ctx context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error)
}

type FunctionImageHooks struct {
	ResolveBase func(ctx context.Context, framework appbuild.Framework) (v1.Image, error)
	ReadRuntime func(ctx context.Context, framework appbuild.Framework) ([]byte, error)
}
