package gcp

import (
	"context"
	"encoding/json"
	"slices"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/platform/gcp/provider/component"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/release"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
	"github.com/ocelhq/ocel/platform/gcp/provider/network"
)

type networkUse struct {
	pass      transform.Pass
	registry  component.Registry
	allowed   map[string]surface.Allowed
	own       []string
	evaluated *sync.Map
}

func (p *Provider) newNetworkUse(deploy provider.DeploySpec) networkUse {
	named := names.New(p.Namespace, p.Options.Project)
	return networkUse{
		pass:      p.transforms,
		registry:  p.Components(),
		allowed:   p.transformed(),
		evaluated: &p.evaluatedPlacements,
		own: []string{
			named.Network(deploy.Tier), named.NetworkPath(deploy.Tier),
			named.Subnetwork(deploy.Tier), named.SubnetworkPath(p.Options.Region, deploy.Tier),
		},
	}
}

func (u networkUse) isOwn(named []string) bool {
	return slices.ContainsFunc(named, func(each string) bool {
		return each != "" && slices.ContainsFunc(u.own, func(own string) bool { return each == own || strings.HasSuffix(each, "/"+own) })
	})
}

func (u networkUse) reachesOverTheNetwork(resource provider.Resource) bool {
	each, served := u.registry.For(resource.Type)
	return served && each.Egress
}

func placementOf(kind provider.BindingType) string {
	switch kind {
	case provider.BindingPostgres:
		return surface.DatabasePlacement
	case provider.BindingKV:
		return surface.StorePlacement
	}
	return ""
}

func appServices(app provider.AppEntry) []transform.Resource {
	var functions []string
	for _, function := range app.Manifest.GetServerless().GetFunctions() {
		functions = append(functions, function.GetLogicalName())
	}
	return release.ServiceCandidates(app.App, app.Compute(), functions, workerNamesOf(app))
}

type placement struct {
	candidate transform.Resource
	path      string
}

func (u networkUse) needsTierNetwork(ctx context.Context, pre provider.DeployPreflight) (bool, error) {
	if u.pass == nil {
		return true, nil
	}
	var reads []placement
	for _, resource := range pre.Resources {
		if !u.reachesOverTheNetwork(resource) {
			continue
		}
		each, _ := u.registry.For(resource.Type)
		path := placementOf(resource.Type)
		target := each.TransformOf(resource.Type)
		if resource.Binding != "" || target == nil || path == "" || !target.Allowed.HasPlacement(path) {
			return true, nil
		}
		reads = append(reads, placement{candidate: transform.Resource{Type: target.Kind, Name: resource.Name}, path: path})
	}
	for _, usage := range pre.Apps {
		if !slices.ContainsFunc(usage.Resources, u.reachesOverTheNetwork) {
			continue
		}
		at := slices.IndexFunc(pre.Deploy.Apps, func(app provider.AppEntry) bool { return app.App == usage.App })
		if at < 0 {
			return true, nil
		}
		for _, service := range appServices(pre.Deploy.Apps[at]) {
			if !u.allowed[service.Type].HasPlacement(surface.EgressPlacement) {
				return true, nil
			}
			reads = append(reads, placement{candidate: service, path: surface.EgressPlacement})
		}
	}
	candidates := make([]transform.Resource, len(reads))
	for i, read := range reads {
		candidates[i] = read.candidate
	}
	evaluated, err := u.evaluate(ctx, surface.Request(pre.Deploy.Tier, pre.Deploy.Env, candidates))
	if err != nil {
		return false, err
	}
	for _, read := range reads {
		named, set := surface.ReadPlacement(evaluated.ForApp(read.candidate.App, read.candidate.Type, read.candidate.Name), read.path)
		if !set || u.isOwn(named) {
			return true, nil
		}
	}
	return false, nil
}

func (u networkUse) evaluate(ctx context.Context, req transform.Request) (surface.Evaluated, error) {
	if u.evaluated == nil {
		return surface.Evaluate(ctx, u.pass, req, u.allowed)
	}
	key, err := json.Marshal(req)
	if err != nil {
		return surface.Evaluated{}, err
	}
	if evaluated, done := u.evaluated.Load(string(key)); done {
		return evaluated.(surface.Evaluated), nil
	}
	evaluated, err := surface.Evaluate(ctx, u.pass, req, u.allowed)
	if err != nil {
		return surface.Evaluated{}, err
	}
	u.evaluated.Store(string(key), evaluated)
	return evaluated, nil
}

func (p *Provider) partsUsedBy(ctx context.Context, pre provider.DeployPreflight) ([]string, error) {
	parts, err := newDeployUse(pre).partNames(p.Components())
	if err != nil {
		return nil, err
	}
	return p.withoutUnusedNetwork(ctx, pre, parts)
}

func (p *Provider) withoutUnusedNetwork(ctx context.Context, pre provider.DeployPreflight, parts []string) ([]string, error) {
	if !slices.Contains(parts, network.PartName) {
		return parts, nil
	}
	needed, err := p.newNetworkUse(pre.Deploy).needsTierNetwork(ctx, pre)
	if err != nil || needed {
		return parts, err
	}
	return slices.DeleteFunc(parts, func(part string) bool { return part == network.PartName }), nil
}
