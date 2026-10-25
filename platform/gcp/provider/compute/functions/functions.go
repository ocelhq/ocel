package functions

import (
	"context"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/next"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/release"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/cloudrun"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/postgres"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/topic"
)

type Functions struct{ *release.Release }

func (p Functions) Provision(ctx context.Context, spec provider.StackSpec, progress progress.Log) ([]provider.Function, error) {
	app := spec.App
	if app == nil {
		return nil, nil
	}
	if next.Serves(app) {
		if err := next.RefuseGuardWithoutShieldingEdge(spec); err != nil {
			return nil, err
		}
	}
	for _, fn := range app.Functions {
		if err := cloudrun.RefuseGateOnFunction(spec, fn); err != nil {
			return nil, err
		}
	}
	budget := postgres.ConnectionBudgetOf(spec, len(app.Functions)+len(app.Workers))
	instances := provider.Instances{Max: budget.Ceiling()}
	budget.WarnOversubscribed(progress)
	facts, err := p.ReadEdgeFacts(ctx, spec)
	if err != nil {
		return nil, err
	}
	c, err := p.OpenClients(ctx)
	if err != nil {
		return nil, err
	}
	named := c.Names
	tasks, declared, err := p.Topics.Tasks(ctx, c, spec)
	if err != nil {
		return nil, err
	}
	var gated cloudrun.GatedServing
	if slices.ContainsFunc(app.Functions, func(fn provider.FunctionSpec) bool { return !cloudrun.IgnoresGate(spec, fn) }) {
		if gated, err = cloudrun.ProvisionGate(ctx, c, spec, p.GateInEnv); err != nil {
			return nil, err
		}
	}
	account, databases, err := p.EnsureAccount(ctx, c, spec, declared)
	if err != nil {
		return nil, err
	}
	own, err := p.RuntimeEnv(named, spec, tasks, databases)
	if err != nil {
		return nil, err
	}
	labels, err := names.LabelsFor(named, spec.Ref, names.AppLabel, app.App)
	if err != nil {
		return nil, err
	}
	if next.Serves(app) && app.ISR != nil {
		if err := next.SeedPrerenders(ctx, p.Objects, spec, progress); err != nil {
			return nil, err
		}
	}
	if err := p.Objects.UploadStaticFiles(ctx, spec, progress); err != nil {
		return nil, err
	}
	deployed := make([]provider.Function, 0, len(app.Functions)+len(app.Workers))
	for _, fn := range app.Functions {
		if err := run.RefuseNotX8664(fn.Framework.Arch, "function "+fn.Name); err != nil {
			return nil, err
		}
		image := functioncode.FunctionImageRef(spec.Images, fn.Name)
		if strings.TrimSpace(image) == "" {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"function %s names no image, and a function on Cloud Run is the container a registry coordinate names", fn.Name)
		}
		service, err := release.NameService(named, spec, app, fn.Name)
		if err != nil {
			return nil, err
		}
		values, err := run.MergeValues(fn.Name, release.SpecValues, app.Values.ContainerEnv, fn.Env)
		if err != nil {
			return nil, err
		}
		if values, err = run.MergeValues(fn.Name, release.RuntimeValues, values, own); err != nil {
			return nil, err
		}
		if values, err = run.MergeValues(fn.Name, release.CachingEdgeValues, values, newCachingEdgeEnv(spec)); err != nil {
			return nil, err
		}
		if values, err = run.MergeValues(fn.Name, release.OcelRouteTableValues, values, newOcelRouteTableEnv(spec)); err != nil {
			return nil, err
		}
		served := run.Serving{
			Service:          service,
			Tier:             spec.Ref.Tier,
			Image:            image,
			Account:          account,
			Compute:          provider.ComputeServerless,
			Public:           true,
			Billing:          p.ResolveBilling(app.App, provider.ComputeServerless, next.Serves(app)),
			Ingress:          run.IngressFor(run.FactsOf(spec.Edge)),
			Memory:           fn.Memory,
			Instances:        instances,
			Egress:           p.EgressFor(named, spec),
			Tag:              p.RevisionTag(spec),
			Labels:           labels,
			OpensOnPromotion: !run.FactsOf(spec.Edge).RunsCode,
		}
		if next.Serves(app) {
			served = next.FillServingDefaults(served)
			buckets, err := c.TierBuckets(ctx, spec.Ref.Tier, naming.StorageEdge, naming.StorageCache)
			if err != nil {
				return nil, err
			}
			database, err := c.Workload().ReadProjectDatabase(ctx, spec.Ref.Tier, spec.Ref.Project)
			if err != nil {
				return nil, err
			}
			if values, err = run.MergeValues(fn.Name, release.NextValues, values, next.NewEnv(spec, fn, served, next.NewCache(named, buckets, database, p.ContainerEndpoint, p.ContainerTagEndpoint, facts.PurgeSecret))); err != nil {
				return nil, err
			}
		}
		if values, err = run.MergeValues(fn.Name, release.CDNPurgeValues, values, next.NewCDNPurgeEnv(named, spec)); err != nil {
			return nil, err
		}
		if values, err = run.MergeValues(fn.Name, release.NodePurgeValues, values, next.NewNodePurgeEnv(spec, facts.PurgeSecret)); err != nil {
			return nil, err
		}
		if values, err = run.MergeValues(fn.Name, release.OriginGuardValues, values, release.NewOriginGuardEnv(facts.OriginSecret)); err != nil {
			return nil, err
		}
		served.Env = values
		gateIgnored := cloudrun.IgnoresGate(spec, fn)
		if !gateIgnored {
			if err := cloudrun.ApplyGate(gated, fn.Name, &served); err != nil {
				return nil, err
			}
		}
		ran, err := p.Services.DeployService(ctx, served, progress)
		if err != nil {
			return nil, err
		}
		release.WarnPreviewOpen(spec, service, progress)
		address, err := release.ReadAddress(spec, "function "+fn.Name, ran)
		if err != nil {
			return nil, err
		}
		deployed = append(deployed, provider.Function{
			Name: fn.Name, Physical: service, URL: address, Revision: ran.Revision, IgnoresGate: gateIgnored,
		})
	}
	if !topic.HostsWorkers(app) {
		return deployed, nil
	}
	values, err := run.MergeValues(app.App, release.RuntimeValues, app.Values.ContainerEnv, own)
	if err != nil {
		return nil, err
	}
	image, err := workerImageOf(app, spec.Images)
	if err != nil {
		return nil, err
	}
	workers, err := p.ProvisionWorkers(ctx, c, spec, image, account, values, declared, instances, progress)
	if err != nil {
		return nil, err
	}
	return append(deployed, release.WorkerFunctions(workers)...), nil
}

func workerImageOf(app *provider.AppSpec, images provider.ImagePushes) (string, error) {
	if at := slices.IndexFunc(app.Functions, func(fn provider.FunctionSpec) bool { return fn.HasWorkerEntry || fn.Name == app.App }); at >= 0 {
		return functioncode.FunctionImageRef(images, app.Functions[at].Name), nil
	}
	if app.Image == "" {
		return "", refusal.Refuse(refusal.CodeInvalid, "app %s hosts workers and ships neither an image nor a function carrying a worker entry to start them from", app.App)
	}
	return app.Image, nil
}

func (p Functions) Remove(ctx context.Context, ref provider.StackRef, functions []provider.Function, images provider.ImageStore, progress progress.Log) error {
	if err := p.TearDownAll(ctx, ref, functionRevisions(functions), images, progress); err != nil {
		return err
	}
	c, err := p.OpenClients(ctx)
	if err != nil {
		return err
	}
	return p.RevokeAfterRemoval(ctx, c, ref, functionNames(functions), nil, progress)
}

func (p Functions) Name(ctx context.Context, spec provider.StackSpec) ([]provider.Function, error) {
	if spec.App == nil {
		return nil, nil
	}
	names, err := p.ReadNames(ctx)
	if err != nil {
		return nil, err
	}
	functions := make([]provider.Function, 0, len(spec.App.Functions)+len(spec.App.Workers))
	for _, fn := range spec.App.Functions {
		service, err := release.NameService(names, spec, spec.App, fn.Name)
		if err != nil {
			return nil, err
		}
		functions = append(functions, provider.Function{Name: fn.Name, Physical: service, IgnoresGate: cloudrun.IgnoresGate(spec, fn)})
	}
	workers, err := release.NameWorkers(names, spec)
	if err != nil {
		return nil, err
	}
	return append(functions, release.WorkerFunctions(workers)...), nil
}

func (p Functions) RemoveRevisions(ctx context.Context, ref provider.StackRef, functions []provider.Function, images provider.ImageStore, progress progress.Log) ([]provider.Function, error) {
	kept, err := p.Release.RemoveRevisions(ctx, ref, functionRevisions(functions), images, progress)
	if err != nil {
		return nil, err
	}
	var left []provider.Function
	for at, function := range functions {
		if slices.Contains(kept, at) {
			left = append(left, function)
		}
	}
	return left, nil
}

func functionRevisions(functions []provider.Function) []release.ServiceRevision {
	revisions := make([]release.ServiceRevision, 0, len(functions))
	for _, function := range functions {
		revisions = append(revisions, release.ServiceRevision{Service: function.Physical, Revision: function.Revision})
	}
	return revisions
}
func functionNames(functions []provider.Function) []string {
	names := make([]string, 0, len(functions))
	for _, function := range functions {
		names = append(names, function.Name)
	}
	return names
}
