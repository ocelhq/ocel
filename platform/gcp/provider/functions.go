package gcp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

func (p *Provider) ProvisionFunctions(ctx context.Context, spec provider.StackSpec, progress progress.Log) ([]provider.Function, error) {
	app := spec.App
	if app == nil {
		return nil, nil
	}
	if servesNext(app) {
		if err := refuseGuardWithoutShieldingEdge(spec); err != nil {
			return nil, err
		}
	}
	for _, fn := range app.Functions {
		if err := refuseGateOnFunction(spec, fn); err != nil {
			return nil, err
		}
	}
	budget := connectionBudgetOf(spec, len(app.Functions)+len(app.Workers))
	instances := provider.Instances{Max: budget.ceiling()}
	budget.warnOversubscribed(progress)
	facts, err := p.readEdgeFacts(ctx, spec)
	if err != nil {
		return nil, err
	}
	c, err := p.openClients(ctx)
	if err != nil {
		return nil, err
	}
	named := c.Names
	tasks, declared, err := p.tasksFor(ctx, c, spec)
	if err != nil {
		return nil, err
	}
	var gated gatedServing
	if slices.ContainsFunc(app.Functions, func(fn provider.FunctionSpec) bool { return !ignoresGate(spec, fn) }) {
		if gated, err = p.provisionGate(ctx, c, spec); err != nil {
			return nil, err
		}
	}
	account, err := p.ensureAppAccount(ctx, c, spec, declared)
	if err != nil {
		return nil, err
	}
	own, err := p.runtimeEnv(named, spec, tasks)
	if err != nil {
		return nil, err
	}
	if servesNext(app) && app.ISR != nil {
		if err := p.seedPrerenders(ctx, spec, progress); err != nil {
			return nil, err
		}
	}
	if err := p.uploadStaticFiles(ctx, spec, progress); err != nil {
		return nil, err
	}
	deployed := make([]provider.Function, 0, len(app.Functions)+len(app.Workers))
	for _, fn := range app.Functions {
		if err := runsX8664(fn.Framework.Arch, "function "+fn.Name); err != nil {
			return nil, err
		}
		image := functioncode.FunctionImageRef(spec.Images, fn.Name)
		if strings.TrimSpace(image) == "" {
			return nil, refusal.Refuse(refusal.CodeInvalid,
				"function %s named no image, and a function on Cloud Run is the container a registry coordinate named", fn.Name)
		}
		service, err := serviceFor(named, spec, app, fn.Name)
		if err != nil {
			return nil, err
		}
		values, err := mergeValues(fn.Name, specValues, app.Values.ContainerEnv, fn.Env)
		if err != nil {
			return nil, err
		}
		if values, err = mergeValues(fn.Name, runtimeValues, values, own); err != nil {
			return nil, err
		}
		if values, err = mergeValues(fn.Name, cachingEdgeValues, values, newCachingEdgeEnv(spec)); err != nil {
			return nil, err
		}
		if values, err = mergeValues(fn.Name, ocelRouteTableValues, values, newOcelRouteTableEnv(spec)); err != nil {
			return nil, err
		}
		served := run.Serving{
			Service:          service,
			Image:            image,
			Account:          account,
			Compute:          provider.ComputeServerless,
			Public:           true,
			InstanceBilled:   servesNext(app),
			Ingress:          run.IngressFor(run.FactsOf(spec.Edge)),
			Memory:           fn.Memory,
			Instances:        instances,
			Egress:           p.egressFor(named, spec),
			Tag:              p.revisionTag(spec),
			Labels:           names.LabelsFor(named, spec.Ref, names.AppLabel, app.App),
			OpensOnPromotion: !run.FactsOf(spec.Edge).RunsCode,
		}
		if servesNext(app) {
			served = fillNextServingDefaults(served)
			if values, err = mergeValues(fn.Name, nextValues, values, newNextEnv(spec, fn, served, newNextCache(named, spec.Ref.Tier, p.containerEndpoint(), p.containerTagEndpoint(), facts.purgeSecret))); err != nil {
				return nil, err
			}
		}
		if values, err = mergeValues(fn.Name, cdnPurgeValues, values, newCDNPurgeEnv(named, spec)); err != nil {
			return nil, err
		}
		if values, err = mergeValues(fn.Name, nodePurgeValues, values, newNodePurgeEnv(spec, facts.purgeSecret)); err != nil {
			return nil, err
		}
		served.Env = values
		gateIgnored := ignoresGate(spec, fn)
		if !gateIgnored {
			if err := applyGate(ctx, c, gated, fn.Name, &served); err != nil {
				return nil, err
			}
		}
		ran, err := p.services().DeployService(ctx, served, progress)
		if err != nil {
			return nil, err
		}
		warnPreviewOpen(spec, service, progress)
		address := ran.Url
		if facts.originBase != "" {
			if served.Tag == "" {
				return nil, fmt.Errorf("function %s is served behind the Cloudflare worker, which reaches the revision this release tags, and this release tagged none", fn.Name)
			}
			if address, err = p.routeOriginHost(ctx, spec, facts.originBase, service, served.Tag, ran.Revision, progress); err != nil {
				return nil, err
			}
		}
		deployed = append(deployed, provider.Function{
			Name: fn.Name, Physical: service, URL: address, Revision: ran.Revision, IgnoresGate: gateIgnored,
		})
	}
	if !hostsWorkers(app) {
		return deployed, nil
	}
	values, err := mergeValues(app.App, runtimeValues, app.Values.ContainerEnv, own)
	if err != nil {
		return nil, err
	}
	image, err := workerImageOf(app, spec.Images)
	if err != nil {
		return nil, err
	}
	workers, err := p.provisionWorkers(ctx, c, spec, image, account, values, declared, instances, progress)
	if err != nil {
		return nil, err
	}
	return append(deployed, workerFunctions(workers)...), nil
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

func (p *Provider) RemoveFunctions(ctx context.Context, ref provider.StackRef, functions []provider.Function, images provider.ImageStore, progress progress.Log) error {
	if err := p.unrouteServiceOriginHosts(ctx, ref.Tier, functions); err != nil {
		return err
	}
	if err := p.tearDownAll(ctx, ref, functionRevisions(functions), images, progress); err != nil {
		return err
	}
	c, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	return p.revokeAfterRemoval(ctx, c, ref, functionNames(functions), nil, progress)
}

func (p *Provider) NameFunctions(ctx context.Context, spec provider.StackSpec) ([]provider.Function, error) {
	if spec.App == nil {
		return nil, nil
	}
	names, err := p.Names(ctx)
	if err != nil {
		return nil, err
	}
	functions := make([]provider.Function, 0, len(spec.App.Functions)+len(spec.App.Workers))
	for _, fn := range spec.App.Functions {
		service, err := serviceFor(names, spec, spec.App, fn.Name)
		if err != nil {
			return nil, err
		}
		functions = append(functions, provider.Function{Name: fn.Name, Physical: service, IgnoresGate: ignoresGate(spec, fn)})
	}
	return append(functions, workerFunctions(nameWorkers(names, spec))...), nil
}

func (p *Provider) RemoveFunctionRevisions(ctx context.Context, ref provider.StackRef, functions []provider.Function, images provider.ImageStore, progress progress.Log) ([]provider.Function, error) {
	kept, err := p.removeRevisions(ctx, ref, functionRevisions(functions), images, progress)
	if err != nil {
		return nil, err
	}
	var left, removed []provider.Function
	for at, function := range functions {
		if slices.Contains(kept, at) {
			left = append(left, function)
			continue
		}
		removed = append(removed, function)
	}
	if err := p.unrouteRevisionOriginHosts(ctx, ref.Tier, removed); err != nil {
		return nil, err
	}
	return left, nil
}

func functionRevisions(functions []provider.Function) []serviceRevision {
	revisions := make([]serviceRevision, 0, len(functions))
	for _, function := range functions {
		revisions = append(revisions, serviceRevision{service: function.Physical, revision: function.Revision})
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
