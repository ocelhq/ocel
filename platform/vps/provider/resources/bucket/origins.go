package bucket

import (
	"context"
	"slices"
	"strings"
	"sync"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

func (r Resource) bucketOrigins(ctx context.Context, ref provider.StackRef, spec *provider.BucketSpec) ([]string, error) {
	claims, err := r.box.Routing.Claims(ctx)
	if err != nil {
		return nil, err
	}
	return corsOrigins(ref, declaredOrigins(spec), claims), nil
}

func corsOrigins(ref provider.StackRef, declared []string, claims []boxapi.Claim) []string {
	origins := slices.Clone(declared)
	owner := boxapi.Surface(ref.Project, string(ref.Tier))
	for _, claim := range claims {
		if claim.Owner != owner || claim.App == boxapi.StoreLabel {
			continue
		}
		if origin := "https://" + claim.Hostname; !slices.Contains(origins, origin) {
			origins = append(origins, origin)
		}
	}
	return origins
}

func (r Resource) ApplyOrigins(ctx context.Context, project string, tier environment.Tier) error {
	entries, err := stackrecords.List(ctx, r.box.KeyValues, tier, project)
	if err != nil {
		return err
	}
	claims := sync.OnceValues(func() ([]boxapi.Claim, error) { return r.box.Routing.Claims(ctx) })
	for _, entry := range entries {
		ref := provider.StackRef{Project: project, Tier: tier, Name: entry.Name}
		if err := r.applyStackOrigins(ctx, ref, entry, claims); err != nil {
			return err
		}
	}
	return nil
}

func (r Resource) applyStackOrigins(ctx context.Context, ref provider.StackRef, entry stackrecords.NamedStack, claims func() ([]boxapi.Claim, error)) error {
	buckets := slices.DeleteFunc(slices.Clone(entry.Bindings), func(binding provider.Binding) bool {
		return binding.Type != provider.BindingBucket || r.stores.forgotten(entry.Name, binding.Name)
	})
	if len(buckets) == 0 {
		return nil
	}
	claimed, err := claims()
	if err != nil {
		return err
	}
	store, err := StoreName(ref)
	if err != nil {
		return err
	}
	root, err := r.storeRoot(ctx, ref, store)
	if err != nil || root.secret == "" {
		return err
	}
	for _, binding := range buckets {
		if err := r.objects.applyOrigins(ctx, storeBucketSpec(ref, store, root.secret, Spec{
			Bucket:         binding.Properties[provider.PropertyBucket],
			AllowedOrigins: corsOrigins(ref, recordedOrigins(binding), claimed),
		})); err != nil {
			return err
		}
	}
	return nil
}

func recordedOrigins(binding provider.Binding) []string {
	return strings.Fields(binding.Properties[stackrecords.PropertyAllowedOrigins])
}
