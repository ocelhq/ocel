package deploy

import (
	"context"
	"crypto/ed25519"
	"fmt"
	"slices"

	sdk "github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

func placeRealtimeAuthorizer(ctx context.Context, cfg Config) (payloads.Placement, error) {
	if cfg.ArtifactBucket == "" {
		return payloads.Placement{}, fmt.Errorf("no artifact bucket to place the realtime authorizer into; re-run `%s`", provider.BootstrapCommand(cfg.Tier))
	}
	return payloads.Place(ctx, cfg.Objects, cfg.ArtifactBucket, realtimeAuthorizerKeyPrefix, "realtime authorizer", payloads.RealtimeAuthorizer())
}

func provisionsRealtime(spec provider.StackSpec) bool {
	return len(realtimeResourcesOf(spec.Resources)) > 0
}

func (r *release) declareRealtime(pctx *sdk.Context, project, env string, resources []provider.Resource, work *infraWork) error {
	realtimes := realtimeResourcesOf(resources)
	if len(realtimes) == 0 {
		return nil
	}
	args := realtimeArgs{Authorizer: work.authorizer, BoundaryARN: r.cfg.AppBoundaryARN}
	for _, resource := range realtimes {
		secret := signingKeySecret(r.cfg.SigningKeyRoot, project, env, resource.Name)
		var seed []byte
		var err error
		if work.previewing {
			seed, err = previewSigningKey(pctx.Context(), r.cfg.SigningKeys, secret)
		} else {
			seed, err = ensureSigningKey(pctx.Context(), r.cfg.SigningKeys, secret)
		}
		if err != nil {
			return fmt.Errorf("declare %s: %w", resource.Name, err)
		}
		args.Namespaces = append(args.Namespaces, realtimeNamespace{
			LogicalName:      resource.Name,
			Namespace:        resource.Declared,
			VerifyKey:        ed25519.NewKeyFromSeed(seed).Public().(ed25519.PublicKey),
			SigningKeySecret: secret,
		})
	}
	return registerRealtime(pctx, project, env, args)
}

func (r *release) listSigningKeys(ctx context.Context, ref provider.StackRef) ([]string, error) {
	return listSigningKeys(ctx, r.cfg.SigningKeys, signingKeyPath(r.cfg.SigningKeyRoot, naming.Sanitize(ref.Project), ref.Name.Env))
}

func (r *release) findUndeclaredSigningKeys(listed []string, spec provider.StackSpec) []string {
	project, env := naming.Sanitize(spec.Ref.Project), spec.Ref.Name.Env
	var undeclared []string
	for _, name := range listed {
		declared := slices.ContainsFunc(realtimeResourcesOf(spec.Resources), func(resource provider.Resource) bool {
			return signingKeySecret(r.cfg.SigningKeyRoot, project, env, resource.Name) == name
		})
		if !declared {
			undeclared = append(undeclared, name)
		}
	}
	return undeclared
}
