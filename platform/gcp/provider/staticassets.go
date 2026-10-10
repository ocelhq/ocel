package gcp

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"slices"
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
	"github.com/ocelhq/ocel/pkg/provider/resources"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type staticFile struct {
	src, key string
}

type staticKeys struct {
	assets      string
	imageConfig string
	appPrefix   string
}

func newStaticKeys(assetPrefix string) (staticKeys, error) {
	segments := strings.Split(assetPrefix, naming.PathSeparator)
	if len(segments) != 5 || segments[4] != naming.AssetsSegment || slices.Contains(segments, "") {
		return staticKeys{}, refusal.Refuse(refusal.CodeInvalid,
			"the asset prefix %q is not <env>/<project>/<app>/<release token>/%s, where the router reads a release's static files and its image config sits beside them",
			assetPrefix, naming.AssetsSegment)
	}
	release := path.Join(segments[:4]...)
	return staticKeys{
		assets:      assetPrefix,
		imageConfig: path.Join(release, naming.ImageConfigFile),
		appPrefix:   provider.StoreAssets + "/" + path.Join(segments[:3]...) + "/",
	}, nil
}

func servesStaticFiles(spec provider.StackSpec) bool {
	return spec.App != nil && servesNext(spec.App) && spec.App.Routing != nil
}

func (p *Provider) uploadStaticFiles(ctx context.Context, spec provider.StackSpec, runProgress progress.Log) error {
	if !servesStaticFiles(spec) {
		return nil
	}
	keys, err := newStaticKeys(spec.App.AssetPrefix)
	if err != nil {
		return err
	}
	root, err := buildoutput.Root(p.projectDir)
	if err != nil {
		return err
	}
	appRoot := buildoutput.AppRoot(root, spec.App.App)
	files, err := staticFilesOf(appRoot, keys)
	if err != nil {
		return err
	}
	if len(files) == 0 {
		return nil
	}

	ensureProgress(runProgress).Say("Uploading " + spec.App.App + "'s " + staticFileCount(len(files)))
	group, ctx := errgroup.WithContext(ctx)
	group.SetLimit(resources.UploadConcurrency)
	for _, file := range files {
		group.Go(func() error {
			defer resources.TakeUploadSlot()()
			body, err := os.ReadFile(file.src)
			if err != nil {
				return fmt.Errorf("static file %s: %w", file.key, err)
			}
			attrs := storage.ObjectAttrs{ContentType: contenttype.Infer(file.key)}
			if file.key == keys.imageConfig {
				return p.replaceObject(ctx, spec.Ref.Tier, provider.StoreAssets, file.key, attrs, body)
			}
			attrs.CacheControl = spec.App.Static.CacheControl("/" + strings.TrimPrefix(file.key, keys.assets+"/"))
			return p.createObject(ctx, spec.Ref.Tier, provider.StoreAssets, file.key, attrs, body)
		})
	}
	return group.Wait()
}

func staticFilesOf(appRoot string, keys staticKeys) ([]staticFile, error) {
	dir := filepath.Join(appRoot, edge.StaticAssetDir)
	rels, err := images.ArtifactFiles(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	files := make([]staticFile, 0, len(rels)+1)
	for _, rel := range rels {
		files = append(files, staticFile{src: filepath.Join(dir, filepath.FromSlash(rel)), key: path.Join(keys.assets, rel)})
	}
	config := filepath.Join(appRoot, naming.ImageConfigFile)
	switch _, err := os.Stat(config); {
	case err == nil:
		files = append(files, staticFile{src: config, key: keys.imageConfig})
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
