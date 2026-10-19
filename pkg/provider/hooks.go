package provider

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/progress"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

type Hooks struct {
	Preflight            *PreflightHooks
	CheckBucket          func(ctx context.Context, record *bindingsv1.BucketProperties, public bool, origins []string) ([]string, error)
	ProgramEdge          func(ctx context.Context, req EdgeProgramRequest) (edge.Program, error)
	EnsureImageRegistry  func(ctx context.Context, tier environment.Tier) (RegistryTarget, error)
	OpenRegistryImages   func(ctx context.Context, target RegistryTarget) (ImageStore, error)
	OpenDirectImages     func(ctx context.Context) (ImageStore, error)
	CheckHost            func(ctx context.Context, req HostCheckRequest) ([]HostCheck, error)
	NextServerRuntime    func(ctx context.Context) (map[string][]byte, error)
	BindingAccess        *BindingAccessHooks
	EnvSource            *EnvSourceHooks
	Cost                 *CostHooks
	Retirement           *RetirementHooks
	ProjectRemoval       *ProjectRemovalHooks
	ListProjectResources func(ctx context.Context, req ProjectResourcesRequest) ([]PhysicalResource, error)
}

type PreflightHooks struct {
	Plan  func(ctx context.Context, pre DeployPreflight) (Plan, error)
	Apply func(ctx context.Context, pre DeployPreflight) error
}

type BindingAccessHooks struct {
	ForwardPorts func(ctx context.Context, req PortForwardRequest, progress progress.Log) ([]PortForward, error)
	ServeProxy   func(ctx context.Context, req BindingProxyRequest, progress progress.Log) (BindingProxy, error)
}

type EnvSourceHooks struct {
	ProveIdentity func(ctx context.Context, audience string) (envsource.IdentityProof, error)
	EnsureSync    func(ctx context.Context, tier environment.Tier, project string) error
}

type CostHooks struct {
	Shape    func(ctx context.Context, req ShapeRequest) (*costv1.ResourceSet, error)
	Estimate func(ctx context.Context, req *costv1.PriceRequest) (*costv1.Estimate, error)
}

type RetirementHooks struct {
	Plan   func(ctx context.Context, live LiveApps, progress progress.Log) (Plan, error)
	Retire func(ctx context.Context, live LiveApps, progress progress.Log) error
}

type ProjectRemovalHooks struct {
	Plan   func(ctx context.Context, tier environment.Tier, project string) ([]ChangeGroup, error)
	Remove func(ctx context.Context, tier environment.Tier, project string, progress progress.Log) error
}
