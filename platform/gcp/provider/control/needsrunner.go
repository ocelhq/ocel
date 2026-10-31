package control

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func refusePlanPreflight(context.Context, provider.DeployPreflight) (provider.Plan, error) {
	return provider.Plan{}, provider.RefuseNeedsRunner("planning a deploy's preflight")
}

func refuseApplyPreflight(context.Context, provider.DeployPreflight) error {
	return provider.RefuseNeedsRunner("applying a deploy's preflight")
}

func refusePlanInfraPreflight(context.Context, provider.DeployPreflight) (provider.Plan, error) {
	return provider.Plan{}, provider.RefuseNeedsRunner("planning a deploy's infrastructure preflight")
}

func refuseApplyInfraPreflight(context.Context, provider.DeployPreflight) error {
	return provider.RefuseNeedsRunner("applying a deploy's infrastructure preflight")
}

func refuseProgramEdge(context.Context, provider.EdgeProgramRequest) (edge.Program, error) {
	return edge.Program{}, provider.RefuseNeedsRunner("programming the edge")
}

func refuseEnsureImageRegistry(context.Context, environment.Tier) (provider.RegistryTarget, error) {
	return provider.RegistryTarget{}, provider.RefuseNeedsRunner("pushing an image")
}

func refuseOpenDirectImages(context.Context) (provider.ImageStore, error) {
	return nil, provider.RefuseNeedsRunner("pushing an image")
}

func refuseEnsureEnvSourceSync(context.Context, environment.Tier, string) error {
	return provider.RefuseNeedsRunner("deploying the environment source sync")
}

func refuseForwardPorts(context.Context, provider.PortForwardRequest, progress.Log) ([]provider.PortForward, error) {
	return nil, provider.RefuseNeedsRunner("forwarding a binding's port")
}

func refuseServeBindingProxy(context.Context, provider.BindingProxyRequest, progress.Log) (provider.BindingProxy, error) {
	return provider.BindingProxy{}, provider.RefuseNeedsRunner("serving a binding proxy")
}

func refuseInventory(context.Context, provider.InventoryRequest) (*costv1.ResourceSet, error) {
	return nil, provider.RefuseNeedsRunner("taking inventory of a deploy")
}

func refusePlanRetirement(context.Context, provider.LiveApps, progress.Log) (provider.Plan, error) {
	return provider.Plan{}, provider.RefuseNeedsRunner("planning the retirement of apps")
}

func refuseRetire(context.Context, provider.LiveApps, progress.Log) error {
	return provider.RefuseNeedsRunner("retiring apps")
}

func refusePlanProjectRemoval(context.Context, environment.Tier, string) ([]provider.ChangeGroup, error) {
	return nil, provider.RefuseNeedsRunner("planning a project's removal")
}

func refuseRemoveProject(context.Context, environment.Tier, string, progress.Log) error {
	return provider.RefuseNeedsRunner("removing a project")
}

func refuseCheckBucket(context.Context, *bindingsv1.BucketProperties, bool, []string) ([]string, error) {
	return nil, provider.RefuseNeedsRunner("checking a bucket's access")
}
