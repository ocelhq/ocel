package release

import (
	"context"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/next"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
)

func (p *Release) NewFunctionEnv(ctx context.Context, c *gcloud.Clients, spec provider.StackSpec, fn provider.FunctionSpec, served run.Serving, own map[string]string, facts EdgeFacts) (map[string]string, error) {
	app := spec.App
	values, err := run.MergeValues(fn.Name, SpecValues, app.Values.ContainerEnv, fn.Env)
	if err != nil {
		return nil, err
	}
	if values, err = run.MergeValues(fn.Name, RuntimeValues, values, own); err != nil {
		return nil, err
	}
	if values, err = run.MergeValues(fn.Name, CachingEdgeValues, values, newCachingEdgeEnv(spec)); err != nil {
		return nil, err
	}
	if values, err = run.MergeValues(fn.Name, OcelRouteTableValues, values, newOcelRouteTableEnv(spec)); err != nil {
		return nil, err
	}
	if next.Serves(app) {
		buckets, err := c.TierBuckets(ctx, spec.Ref.Tier, naming.StorageEdge, naming.StorageCache)
		if err != nil {
			return nil, err
		}
		database, err := c.Workload().ReadProjectDatabase(ctx, spec.Ref.Tier, spec.Ref.Project)
		if err != nil {
			return nil, err
		}
		if values, err = run.MergeValues(fn.Name, NextValues, values, next.NewEnv(spec, fn, served, next.NewCache(c.Names, buckets, database, p.ContainerEndpoint, p.ContainerTagEndpoint, facts.PurgeSecret))); err != nil {
			return nil, err
		}
	}
	if values, err = run.MergeValues(fn.Name, CDNPurgeValues, values, next.NewCDNPurgeEnv(c.Names, spec)); err != nil {
		return nil, err
	}
	if values, err = run.MergeValues(fn.Name, NodePurgeValues, values, next.NewNodePurgeEnv(spec, facts.PurgeSecret)); err != nil {
		return nil, err
	}
	return run.MergeValues(fn.Name, OriginGuardValues, values, NewOriginGuardEnv(facts.OriginSecret))
}
