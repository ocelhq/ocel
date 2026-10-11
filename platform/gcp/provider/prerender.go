package gcp

import (
	"context"
	"fmt"
	"os"

	"cloud.google.com/go/storage"
	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/prerender"
	"github.com/ocelhq/ocel/pkg/provider/resources"
)

func (p *Provider) seedPrerenders(ctx context.Context, spec provider.StackSpec, runProgress progress.Log) error {
	root, err := buildoutput.Root(p.projectDir)
	if err != nil {
		return err
	}
	isr := spec.App.ISR
	seeds, err := prerender.Seeds(buildoutput.AppRoot(root, spec.App.App), isr.Prefix)
	if err != nil {
		return err
	}

	if len(seeds) == 0 {
		return nil
	}

	ensureProgress(runProgress).Say("Uploading " + spec.App.App + "'s " + prerenderCount(len(seeds)))
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(resources.UploadConcurrency)
	for _, seed := range seeds {
		group.Go(func() error {
			defer resources.TakeUploadSlot()()
			body, err := os.ReadFile(seed.Path)
			if err != nil {
				return fmt.Errorf("seed %s: %w", seed.Key, err)
			}
			return p.createSeed(ctx, spec, seed.Key, body)
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

func (p *Provider) createSeed(ctx context.Context, spec provider.StackSpec, key string, body []byte) error {
	return p.createObject(ctx, spec.Ref.Tier, provider.StoreCache, key, storage.ObjectAttrs{}, body)
}
