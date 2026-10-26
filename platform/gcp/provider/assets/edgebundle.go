package assets

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"cloud.google.com/go/storage"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/baked"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

type EdgeBundle struct {
	Key      string
	Envelope string
}

func (p *Store) PutEdgeBundle(ctx context.Context, spec provider.StackSpec, runProgress progress.Log) (EdgeBundle, error) {
	if spec.App == nil || spec.App.EdgeRouteTable == nil {
		return EdgeBundle{}, nil
	}
	app := spec.App
	bundle, err := os.ReadFile(filepath.Join(buildoutput.AppRoot(app.OutputRoot, app.App), filepath.FromSlash(edge.AppBundleFile)))
	if errors.Is(err, fs.ErrNotExist) {
		return EdgeBundle{}, nil
	}
	if err != nil {
		return EdgeBundle{}, fmt.Errorf("read %s's edge bundle: %w", app.App, err)
	}
	gcloud.EnsureProgress(runProgress).Say("Uploading " + app.App + "'s edge bundle")
	delivered := EdgeBundle{Key: app.StorageCoordinate.BundleKey(edge.BundleObjectFile)}
	if err := p.CreateObject(ctx, spec.Ref.Tier, delivered.Key, storage.ObjectAttrs{ContentType: "application/json"}, bundle); err != nil {
		return EdgeBundle{}, fmt.Errorf("upload %s's edge bundle: %w", app.App, err)
	}
	if len(app.Values.Sensitive) == 0 {
		return delivered, nil
	}
	sealed, envelope, err := baked.SealUnderNewKey(app.Values.Sensitive)
	if err != nil {
		return EdgeBundle{}, fmt.Errorf("seal %s's edge values: %w", app.App, err)
	}
	sealedKey := app.StorageCoordinate.BundleKey(baked.EdgeFile)
	if err := p.replaceObject(ctx, spec.Ref.Tier, sealedKey, storage.ObjectAttrs{ContentType: "application/octet-stream"}, sealed); err != nil {
		return EdgeBundle{}, fmt.Errorf("upload %s's sealed edge values: %w", app.App, err)
	}
	delivered.Envelope = envelope
	return delivered, nil
}
