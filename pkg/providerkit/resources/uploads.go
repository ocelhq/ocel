package resources

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func UploadRows(ctx context.Context, store provider.ArtifactStore, uploads []provider.Upload) ([]provider.Change, error) {
	rows := make([]provider.Change, 0, len(uploads))
	for _, upload := range uploads {
		held, err := store.Has(ctx, upload.Ref)
		if err != nil {
			return nil, fmt.Errorf("look for %s's artifact: %w", upload.Name, err)
		}
		rows = append(rows, provider.Change{Kind: provider.UploadKind, Name: upload.Name, Action: provider.KeepOrCreate(held)})
	}
	return rows, nil
}

const UploadConcurrency = 64

var uploadSlots = make(chan struct{}, UploadConcurrency)

func TakeUploadSlot() func() {
	uploadSlots <- struct{}{}
	return func() { <-uploadSlots }
}

func ShipUploads(ctx context.Context, store provider.ArtifactStore, uploads []provider.Upload, progress edge.Progress) error {
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(UploadConcurrency)
	for _, upload := range uploads {
		group.Go(func() error {
			defer TakeUploadSlot()()
			return ship(ctx, store, upload, progress)
		})
	}
	return group.Wait()
}

func ship(ctx context.Context, store provider.ArtifactStore, upload provider.Upload, progress edge.Progress) error {
	held, err := store.Has(ctx, upload.Ref)
	if err != nil {
		return fmt.Errorf("look for %s's artifact: %w", upload.Name, err)
	}
	if held {
		return nil
	}
	body, err := os.Open(upload.Path)
	if err != nil {
		return fmt.Errorf("read %s's artifact: %w", upload.Name, err)
	}
	defer body.Close()
	if progress != nil {
		progress.Say("Uploading " + upload.Name)
	}
	if err := store.Put(ctx, upload.Ref, body); err != nil {
		return fmt.Errorf("upload %s's artifact: %w", upload.Name, err)
	}
	return nil
}
