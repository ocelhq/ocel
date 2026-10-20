package assets

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"cloud.google.com/go/storage"
	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/contenttype"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/functioncode"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

type staticFile struct {
	src, key string
}

func servesStaticFiles(spec provider.StackSpec) bool {
	return spec.App != nil && spec.App.Framework == buildoutput.FrameworkNext && spec.App.Compute != provider.ComputeContainer && spec.App.Routing != nil
}

func (p *Store) UploadStaticFiles(ctx context.Context, spec provider.StackSpec, runProgress progress.Log) error {
	if !servesStaticFiles(spec) {
		return nil
	}
	release := spec.App.StorageCoordinate
	imageConfig := release.ImageConfigKey()
	appRoot := buildoutput.AppRoot(spec.App.OutputRoot, spec.App.App)
	files, err := staticFilesOf(appRoot, release)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}

	gcloud.EnsureProgress(runProgress).Say("Uploading " + spec.App.App + "'s " + staticFileCount(len(files)))
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(functioncode.UploadConcurrency)
	for _, file := range files {
		group.Go(func() error {
			defer functioncode.TakeUploadSlot()()
			if file.key != imageConfig {
				present, err := p.Has(ctx, provider.ArtifactRef{Tier: spec.Ref.Tier, Key: file.key})
				if err != nil || present {
					return err
				}
			}
			body, err := os.ReadFile(file.src)
			if err != nil {
				return fmt.Errorf("static file %s: %w", file.key, err)
			}
			attrs := storage.ObjectAttrs{ContentType: contenttype.Infer(file.key)}
			if file.key == imageConfig {
				return p.replaceObject(ctx, spec.Ref.Tier, file.key, attrs, body)
			}
			attrs.CacheControl = spec.App.Static.CacheControl(naming.PathSeparator + strings.TrimPrefix(file.key, release.StoragePrefix(naming.ObjectAssets)))
			return p.CreateObject(ctx, spec.Ref.Tier, file.key, attrs, body)
		})
	}
	return group.Wait()
}

func staticFilesOf(appRoot string, release naming.Coordinate) ([]staticFile, error) {
	dir := filepath.Join(appRoot, edge.StaticAssetDir)
	rels, err := images.ArtifactFiles(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	files := make([]staticFile, 0, len(rels)+1)
	for _, rel := range rels {
		files = append(files, staticFile{src: filepath.Join(dir, filepath.FromSlash(rel)), key: release.AssetKey(rel)})
	}
	config := filepath.Join(appRoot, naming.ImageConfigFile)
	switch _, err := os.Stat(config); {
	case err == nil:
		files = append(files, staticFile{src: config, key: release.ImageConfigKey()})
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	return files, nil
}

func staticFileCount(n int) string {
	if n == 1 {
		return "1 static file"
	}
	return fmt.Sprintf("%d static files", n)
}
