package next

import (
	"context"
	"fmt"
	"os"

	"cloud.google.com/go/storage"
	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"
	"github.com/ocelhq/ocel/pkg/provider/prerender"
	"github.com/ocelhq/ocel/platform/gcp/provider/assets"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

func SeedPrerenders(ctx context.Context, store *assets.Store, spec provider.StackSpec, runProgress progress.Log) error {
	isr := spec.App.ISR
	seeds, err := prerender.Seeds(buildoutput.AppRoot(spec.App.OutputRoot, spec.App.App), isr.Prefix)
	if err != nil {
		return err
	}

	if len(seeds) == 0 {
		return nil
	}

	gcloud.EnsureProgress(runProgress).Say("Uploading " + spec.App.App + "'s " + prerenderCount(len(seeds)))
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(functioncode.UploadConcurrency)
	for _, seed := range seeds {
		group.Go(func() error {
			defer functioncode.TakeUploadSlot()()
			body, err := os.ReadFile(seed.Path)
			if err != nil {
				return fmt.Errorf("seed %s: %w", seed.Key, err)
			}
			return createSeed(ctx, store, spec, seed.Key, body)
		})
	}
	return group.Wait()
}

func prerenderCount(n int) string {
	if n == 1 {
		return "1 prerender cache entry"
	}
	return fmt.Sprintf("%d prerender cache entries", n)
}

func createSeed(ctx context.Context, store *assets.Store, spec provider.StackSpec, key string, body []byte) error {
	return store.CreateObject(ctx, spec.Ref.Tier, provider.StoreCache, key, storage.ObjectAttrs{}, body)
}
