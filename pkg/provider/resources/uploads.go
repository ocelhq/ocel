package resources

import (
	"context"
	"fmt"
	"os"

	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
)

func UploadRows(ctx context.Context, store provider.ArtifactStore, uploads []provider.Upload) ([]provider.Change, error) {
	rows := make([]provider.Change, 0, len(uploads))
	for _, upload := range uploads {
		present, err := store.Has(ctx, upload.Ref)
		if err != nil {
			return nil, fmt.Errorf("look for %s's artifact: %w", upload.Name, err)
		}
		rows = append(rows, provider.Change{Kind: provider.UploadKind, Name: upload.Name, Action: provider.KeepOrCreate(present)})
	}
	return rows, nil
}

const UploadConcurrency = 64

var uploadSlots = make(chan struct{}, UploadConcurrency)

func TakeUploadSlot() func() {
	uploadSlots <- struct{}{}
	return func() { <-uploadSlots }
}

func ShipUploads(ctx context.Context, store provider.ArtifactStore, uploads []provider.Upload, progress progress.Progress) error {
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

func ship(ctx context.Context, store provider.ArtifactStore, upload provider.Upload, progress progress.Progress) error {
	present, err := store.Has(ctx, upload.Ref)
	if err != nil {
		return fmt.Errorf("look for %s's artifact: %w", upload.Name, err)
	}
	if present {
		return nil
	}
	body, err := os.Open(upload.Path)
	if err != nil {
		return fmt.Errorf("read %s's artifact: %w", upload.Name, err)
	}
	defer body.Close()
	if progress != nil {
		message := "Uploading function " + upload.Name + "'s artifact"
		if info, err := body.Stat(); err == nil {
			message += " (" + sizeOf(info.Size()) + ")"
		}
		progress.Say(message)
	}
	if err := store.Put(ctx, upload.Ref, body); err != nil {
		return fmt.Errorf("upload %s's artifact: %w", upload.Name, err)
	}
	return nil
}

func sizeOf(bytes int64) string {
	switch {
	case bytes < 1<<10:
		return fmt.Sprintf("%d B", bytes)
	case bytes < 1<<20:
		return fmt.Sprintf("%.1f KiB", float64(bytes)/(1<<10))
	default:
		return fmt.Sprintf("%.1f MiB", float64(bytes)/(1<<20))
	}
}
