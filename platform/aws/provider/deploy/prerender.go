package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	awshttp "github.com/aws/aws-sdk-go-v2/aws/transport/http"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	smithy "github.com/aws/smithy-go"
	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func storageCoordinate(env, slug, app string, release naming.Release) naming.Coordinate {
	return naming.Coordinate{
		Project: naming.Sanitize(slug),
		Env:     naming.Sanitize(env),
		App:     naming.Sanitize(app),
		Release: release,
	}
}

func isrPrefixOf(c naming.Coordinate) string {
	return strings.TrimSuffix(c.ISRPrefix(), naming.PathSeparator)
}

func bytecodePrefixOf(c naming.Coordinate) string {
	return strings.TrimSuffix(c.BytecodePrefix(), naming.PathSeparator)
}

type appBuilds struct {
	ids        map[string]string
	identities Identities
	coords     map[string]naming.Coordinate
	caches     map[string]*isrConfig
	bytecode   map[string]*bytecodeConfig
}

func resolveAppBuilds(cfg Config, manifest *deploymentsv1.Manifest, baked map[string]appBundle) (appBuilds, error) {
	builds := appBuilds{
		ids:        map[string]string{},
		identities: Identities{},
		coords:     map[string]naming.Coordinate{},
		caches:     map[string]*isrConfig{},
		bytecode:   map[string]*bytecodeConfig{},
	}
	for _, app := range manifestApps(manifest) {
		name := app.GetName()
		buildID, err := builds.resolve(cfg, app)
		if err != nil {
			return appBuilds{}, err
		}
		id, err := NewIdentity(buildID, baked[name].Fingerprint)
		if err != nil {
			return appBuilds{}, fmt.Errorf("deployment identity for %s: %w", name, err)
		}
		builds.identities[name] = id
		coord := storageCoordinate(cfg.Env, manifest.GetSlug(), name, releaseOf(id))
		builds.coords[name] = coord
		builds.bytecode[name] = &bytecodeConfig{
			Bucket: cfg.AssetBucket,
			Prefix: bytecodePrefixOf(coord),
		}
	}
	for _, fn := range manifest.GetFunctions() {
		app := fn.GetApp()
		if fn.GetFramework() != frameworkNext || builds.caches[app] != nil {
			continue
		}
		coord, ok := builds.coords[app]
		if !ok {
			return appBuilds{}, fmt.Errorf("function %s names the app %q, which this manifest does not declare", fn.GetLogicalName(), app)
		}
		prefix := isrPrefixOf(coord)
		cache := &isrConfig{
			Coord:    coord,
			Bucket:   cfg.AssetBucket,
			Prefix:   prefix,
			Table:    cfg.StateTable,
			TableARN: cfg.StateTableARN,
		}
		if isrEntriesAdopted(cfg) {
			cache.CacheStoreBucket = cfg.CacheStoreBucket
			cache.WriterURL = cfg.ISRWriterEndpoint + "/" + prefix + "/entry"
			cache.WriterSecret = isrWriteSecret(cfg.ISRWriterSeed, prefix)
		}
		builds.caches[app] = cache
	}
	return builds, nil
}

func (b appBuilds) resolve(cfg Config, app *deploymentsv1.ManifestApp) (string, error) {
	name := app.GetName()
	if id := b.ids[name]; id != "" {
		return id, nil
	}
	id, err := appBuildID(cfg, app)
	if err != nil {
		return "", err
	}
	b.ids[name] = id
	return id, nil
}

func appBuildID(cfg Config, app *deploymentsv1.ManifestApp) (string, error) {
	name := app.GetName()
	desc, ok, err := readServeDescriptor(cfg.ArtifactRoot, name)
	switch {
	case err != nil:
		return "", err
	case !ok:
		return newRandomID()
	case desc.BuildID == "":
		return "", fmt.Errorf("serve descriptor for %s is missing buildId; rebuild the app", name)
	}
	return desc.BuildID, nil
}

func uploadPrerenderAssets(ctx context.Context, cfg Config, builds appBuilds) error {
	caches := builds.caches

	segments := []struct {
		dir string
		to  uploadTarget
	}{
		{"cache", entryTarget(cfg)},
		{"fetch-cache", uploadTarget{up: cfg.Uploader, bucket: cfg.AssetBucket}},
	}

	type upload struct {
		key, src string
		to       uploadTarget
	}
	var uploads []upload
	for app, cache := range caches {
		for _, seg := range segments {
			dir := filepath.Join(appArtifactRoot(cfg.ArtifactRoot, app), seg.dir)
			entries, err := collectFiles(dir)
			if err != nil {
				return err
			}
			for _, rel := range entries {
				uploads = append(uploads, upload{
					key: path.Join(cache.Prefix, seg.dir, rel),
					src: filepath.Join(dir, filepath.FromSlash(rel)),
					to:  seg.to,
				})
			}
		}
	}
	if err := seedTagSnapshots(ctx, cfg, caches, time.Now()); err != nil {
		return err
	}
	if err := seedISRWriters(ctx, cfg, caches); err != nil {
		return err
	}
	if len(uploads) == 0 {
		return nil
	}

	for _, seg := range segments {
		if err := seg.to.validate(); err != nil {
			return err
		}
	}

	phaseStart := time.Now()
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(uploadConcurrency)
	stats := newUploadBatchStats()
	for _, u := range uploads {
		g.Go(func() error {
			return tracedUpload(ctx, u.to.up, u.to.bucket, u.key, "", func() ([]byte, error) {
				return os.ReadFile(u.src)
			}, stats)
		})
	}
	err := g.Wait()
	emitUploadBatch(cfg.Tracer, cfg.Stages.Uploading.ID, uploadKindPrerenderAsset, stats, err, phaseStart)
	return err
}

type uploadTarget struct {
	up     ArtifactUploader
	bucket string
}

func (t uploadTarget) validate() error {
	if t.bucket == "" {
		return fmt.Errorf("this project has objects to publish but no asset bucket is configured; re-run `ocel bootstrap`")
	}
	if t.up == nil {
		return fmt.Errorf("no asset uploader configured")
	}
	return nil
}

type tagSnapshot struct {
	Version     int                  `json:"version"`
	DeployedAt  int64                `json:"deployedAt"`
	GeneratedAt int64                `json:"generatedAt"`
	Records     map[string]tagRecord `json:"records"`
}

type tagRecord struct {
	Stale   int64 `json:"stale,omitempty"`
	Expired int64 `json:"expired,omitempty"`
}

const (
	tagSnapshotVersion = 1
	tagSnapshotSuffix  = "/tag-clock.json"
)

func genesisSnapshot(at time.Time) tagSnapshot {
	ms := at.UnixMilli()
	return tagSnapshot{
		Version:     tagSnapshotVersion,
		DeployedAt:  ms,
		GeneratedAt: ms,
		Records:     map[string]tagRecord{},
	}
}

func seedTagSnapshots(ctx context.Context, cfg Config, caches map[string]*isrConfig, at time.Time) error {
	body, err := json.Marshal(genesisSnapshot(at))
	if err != nil {
		return fmt.Errorf("encode tag snapshot: %w", err)
	}

	for _, target := range snapshotTargets(cfg) {
		for _, cache := range caches {
			key := cache.Prefix + tagSnapshotSuffix
			_, err := target.up.PutObject(ctx, &s3.PutObjectInput{
				Bucket:      aws.String(target.bucket),
				Key:         aws.String(key),
				Body:        bytes.NewReader(body),
				ContentType: aws.String("application/json"),
				IfNoneMatch: aws.String("*"),
			})
			if err != nil && !isPreconditionFailed(err) {
				return fmt.Errorf("seed tag snapshot %s in %s: %w", key, target.bucket, err)
			}
		}
	}
	return nil
}

func snapshotTargets(cfg Config) []uploadTarget {
	var targets []uploadTarget
	for _, t := range []uploadTarget{{up: cfg.Uploader, bucket: cfg.AssetBucket}, entryTarget(cfg)} {
		if t.validate() != nil {
			continue
		}
		if len(targets) == 1 && targets[0].bucket == t.bucket {
			continue
		}
		targets = append(targets, t)
	}
	return targets
}

func isPreconditionFailed(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) && apiErr.ErrorCode() == "PreconditionFailed" {
		return true
	}
	var respErr *awshttp.ResponseError
	return errors.As(err, &respErr) && respErr.HTTPStatusCode() == http.StatusPreconditionFailed
}

func entryTarget(cfg Config) uploadTarget {
	if isrEntriesAdopted(cfg) {
		return uploadTarget{up: cfg.CacheStoreUploader, bucket: cfg.CacheStoreBucket}
	}
	return uploadTarget{up: cfg.Uploader, bucket: cfg.AssetBucket}
}

func isrEntriesAdopted(cfg Config) bool {
	return cfg.CacheStoreBucket != "" && cfg.CacheStoreUploader != nil
}

func collectFiles(dir string) ([]string, error) {
	var rels []string
	err := filepath.WalkDir(dir, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(dir, p)
		if err != nil {
			return err
		}
		rels = append(rels, filepath.ToSlash(rel))
		return nil
	})
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, nil
		}
		return nil, fmt.Errorf("crawl %s: %w", dir, err)
	}
	return rels, nil
}
