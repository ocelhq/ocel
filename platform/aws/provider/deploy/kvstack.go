package deploy

import (
	"context"
	"maps"
	"slices"

	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (r *release) declareKV(pctx *sdk.Context, project, env string, resource provider.Resource, work *infraWork, vpcID, vpcCIDR string, subnetIDs []string) error {
	args, err := translateKV(resource.Name, resource.KV)
	if err != nil {
		return err
	}
	args.AuthTokenParameter = kvTokenParameter(r.cfg.KVTokenRoot, project, env, resource.Name)
	if work.previewing {
		args.AuthToken, err = previewKVToken(pctx.Context(), r.cfg.Parameters, args.AuthTokenParameter)
	} else {
		args.AuthToken, err = ensureKVToken(pctx.Context(), r.cfg.Parameters, args.AuthTokenParameter)
	}
	if err != nil {
		return err
	}
	args.Tags = work.transformed.tagsFor(transformTypeKV, resource.Name)
	return registerKV(pctx, project, env, resource.Name, args, vpcID, vpcCIDR, subnetIDs)
}

func (r *release) kvTokensProvisioned(ctx context.Context, ref provider.StackRef, progress progress.Log) (map[string]string, error) {
	outputs, err := r.automation.Outputs(ctx, ref, progress)
	if err != nil {
		return nil, err
	}
	tokens := map[string]string{}
	for name, output := range outputs {
		fields, mapped := output.Value.(map[string]any)
		if !mapped {
			continue
		}
		if parameter, ok := fields[outputKeyAuthTokenParameter].(string); ok && parameter != "" {
			tokens[name] = parameter
		}
	}
	return tokens, nil
}

func kvTokensRemoved(prior map[string]string, spec provider.StackSpec) []string {
	removed := maps.Clone(prior)
	for _, resource := range spec.Resources {
		if resource.Type == provider.BindingKV && resource.Binding == "" {
			delete(removed, resource.Name)
		}
	}
	return slices.Sorted(maps.Values(removed))
}
