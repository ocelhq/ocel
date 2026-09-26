package provider

import (
	"context"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/appbuild"
	"github.com/ocelhq/ocel/pkg/providerkit/images"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type Hooks struct {
	PreflightDeploy     func(ctx context.Context, pre DeployPreflight) error
	VerifyGrants        func(ctx context.Context, binding Binding) error
	InspectStack        func(ctx context.Context, ref StackRef) (StackState, error)
	PackApp             func(ctx context.Context, req PackAppRequest, progress edge.Progress) (PackAppResult, error)
	EmbedCode           func(ctx context.Context, function string, artifact ArtifactRef, progress edge.Progress) error
	WarmFunctions       func(ctx context.Context, targets []string, progress edge.Progress) error
	ProgramEdge         func(ctx context.Context, req EdgeProgramRequest) (EdgeProgram, error)
	EnsureImageRegistry func(ctx context.Context, class edge.Class, repositories []string) (images.Registry, error)
	OpenRegistryImages  func(ctx context.Context, target images.Registry) (images.Store, error)
	OpenDirectImages    func(ctx context.Context) (images.Store, error)
	CheckHost           func(ctx context.Context, req HostCheckRequest) ([]HostCheck, error)
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
