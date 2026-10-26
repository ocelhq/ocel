package vps

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/contenttype"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runtime/baked"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

var releaseObjectKinds = []naming.ObjectKind{naming.ObjectAssets, naming.ObjectImageConfig, naming.ObjectRouteTables, naming.ObjectBundles}

type stacks struct {
	provider.Stacks
	p *Provider
}

func isFrontedApp(spec provider.StackSpec) bool {
	return spec.App != nil && spec.Edge != nil && spec.Edge.Kind() == cloudflare.Kind && spec.Edge.Facts().FrontsOrigins
}

func isCloudflareProxiedApp(spec provider.StackSpec) bool {
	return spec.App != nil && spec.Edge != nil && spec.Edge.Kind() == cloudflare.Kind && !spec.Edge.Facts().FrontsOrigins
}

func (s stacks) Provision(ctx context.Context, spec provider.StackSpec, progress progress.Log) (provider.StackResult, error) {
	if isCloudflareProxiedApp(spec) {
		if err := s.p.forgetFrontedProject(ctx, spec.Ref.Tier, naming.Sanitize(spec.Ref.Project)); err != nil {
			return provider.StackResult{}, err
		}
	}
	if !isFrontedApp(spec) {
		return s.Stacks.Provision(ctx, spec, progress)
	}
	held, err := s.p.holdFronting(ctx, spec.Ref.Tier, spec.App.StorageCoordinate.Project)
	if err != nil {
		return provider.StackResult{}, err
	}
	delivered, err := keepReleaseObjects(ctx, held, spec, progress)
	if err != nil {
		return provider.StackResult{}, err
	}
	result, err := s.Stacks.Provision(ctx, spec, progress)
	if err != nil {
		return result, err
	}
	result.RouteTableKey, result.EdgeBundleKey, result.Envelope = delivered.routeTableKey, delivered.bundleKey, delivered.envelope
	result.PurgeSecret = frontedPurgeSecret(held.record, spec)
	return result, nil
}

func frontedPurgeSecret(record frontingRecord, spec provider.StackSpec) string {
	if !spec.App.IsBuiltFromFunction() || spec.App.ISR == nil {
		return ""
	}
	return edge.DerivePurgeSecret(record.PurgeSeed, spec.App.StorageCoordinate.StorageCoordinate())
}

type keptDelivery struct {
	routeTableKey string
	bundleKey     string
	envelope      string
}

func keepReleaseObjects(ctx context.Context, held fronting, spec provider.StackSpec, progress progress.Log) (keptDelivery, error) {
	app := spec.App
	coordinate := app.StorageCoordinate
	root := buildoutput.AppRoot(app.OutputRoot, app.App)
	objects, err := readStaticObjects(root, coordinate)
	if err != nil {
		return keptDelivery{}, err
	}
	var delivered keptDelivery
	if table := app.EdgeRouteTable; table != nil {
		objects = append(objects, cloudflare.KeptObject{Key: table.Location.Key, ContentType: "application/json", Body: table.Table})
		delivered.routeTableKey = table.Location.Key
		bundle, sealed, envelope, err := readEdgeBundle(root, app.Values.Sensitive)
		if err != nil {
			return keptDelivery{}, err
		}
		if bundle != nil {
			delivered.bundleKey = coordinate.BundleKey(edge.BundleObjectFile)
			objects = append(objects, cloudflare.KeptObject{Key: delivered.bundleKey, ContentType: "application/json", Body: bundle})
		}
		if sealed != nil {
			objects = append(objects, cloudflare.KeptObject{Key: coordinate.BundleKey(baked.EdgeFile), ContentType: "application/octet-stream", Body: sealed})
			delivered.envelope = envelope
		}
	}
	if len(objects) == 0 {
		return delivered, nil
	}
	progress.Say(fmt.Sprintf("Keeping %s's %d files at Cloudflare", app.App, len(objects)))
	if err := held.kept.Put(ctx, held.store, coordinate.Project, objects); err != nil {
		return keptDelivery{}, fmt.Errorf("keep %s's files at Cloudflare: %w", app.App, err)
	}
	return delivered, nil
}

func readStaticObjects(root string, coordinate naming.Coordinate) ([]cloudflare.KeptObject, error) {
	dir := filepath.Join(root, edge.StaticAssetDir)
	rels, err := images.ArtifactFiles(dir)
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, err
	}
	objects := make([]cloudflare.KeptObject, 0, len(rels)+1)
	for _, rel := range rels {
		body, err := os.ReadFile(filepath.Join(dir, filepath.FromSlash(rel)))
		if err != nil {
			return nil, fmt.Errorf("read static file %s: %w", rel, err)
		}
		key := coordinate.AssetKey(rel)
		objects = append(objects, cloudflare.KeptObject{Key: key, ContentType: contenttype.Infer(key), Body: body})
	}
	config, err := os.ReadFile(filepath.Join(root, naming.ImageConfigFile))
	switch {
	case err == nil:
		objects = append(objects, cloudflare.KeptObject{Key: coordinate.ImageConfigKey(), ContentType: "application/json", Body: config})
	case !errors.Is(err, fs.ErrNotExist):
		return nil, err
	}
	return objects, nil
}

func readEdgeBundle(root string, sensitive map[string]string) (bundle, sealed []byte, envelope string, err error) {
	bundle, err = os.ReadFile(filepath.Join(root, filepath.FromSlash(edge.AppBundleFile)))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, nil, "", nil
	}
	if err != nil || len(sensitive) == 0 {
		return bundle, nil, "", err
	}
	if sealed, envelope, err = baked.SealUnderNewKey(sensitive); err != nil {
		return nil, nil, "", err
	}
	return bundle, sealed, envelope, nil
}

func (p *Provider) forgetReleaseObjects(ctx context.Context, ref provider.StackRef) error {
	if ref.Name.Release.IsZero() || ref.Name.App == "" {
		return nil
	}
	record, held, err := p.readFronting(ctx, ref.Tier)
	if err != nil || !held {
		return err
	}
	kept, err := p.keptObjects()
	if err != nil {
		return err
	}
	coordinate := naming.Coordinate{Project: naming.Sanitize(ref.Project), Env: ref.Name.Env, App: ref.Name.App, Release: ref.Name.Release}
	prefixes := make([]string, 0, len(releaseObjectKinds))
	for _, kind := range releaseObjectKinds {
		prefixes = append(prefixes, coordinate.StoragePrefix(kind))
	}
	return kept.Remove(ctx, record.releasesStore(), coordinate.Project, prefixes)
}
