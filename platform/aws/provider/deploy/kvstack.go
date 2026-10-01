package deploy

import (
	"context"
	"slices"

	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
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

func (r *release) listKVTokens(ctx context.Context, ref provider.StackRef) ([]string, error) {
	return listKVTokens(ctx, r.cfg.Parameters, kvTokenPath(r.cfg.KVTokenRoot, naming.Sanitize(ref.Project), ref.Name.Env))
}

func (r *release) findUndeclaredKVTokens(listed []string, spec provider.StackSpec) []string {
	project, env := naming.Sanitize(spec.Ref.Project), spec.Ref.Name.Env
	var undeclared []string
	for _, name := range listed {
		declared := slices.ContainsFunc(spec.Resources, func(resource provider.Resource) bool {
			return resource.Type == provider.BindingKV && resource.Binding == "" &&
				kvTokenParameter(r.cfg.KVTokenRoot, project, env, resource.Name) == name
		})
		if !declared {
			undeclared = append(undeclared, name)
		}
	}
	return undeclared
}
