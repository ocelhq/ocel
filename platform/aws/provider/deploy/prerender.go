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
	"github.com/ocelhq/ocel/pkg/providerkit"
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

func publishPrerenderAssets(ctx context.Context, cfg Config, app string, cache *isrConfig, report providerkit.Reporter) error {
	if cache == nil {
		return nil
	}

	segments := []struct {
		dir string
		to  uploadTarget
	}{
		{"cache", entryTarget(cfg)},
		{"fetch-cache", uploadTarget{up: cfg.Uploader, bucket: cfg.AssetBucket, class: cfg.Class}},
	}

	type upload struct {
		key, src string
		to       uploadTarget
	}
	var uploads []upload
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
	if err := seedTagSnapshot(ctx, cfg, cache, time.Now()); err != nil {
		return err
	}
	if err := seedISRWriter(ctx, cfg.isrWriter(), app, cache); err != nil {
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

	report.Say("Uploading " + app + "'s prerendered pages")
	phaseStart := time.Now()
	g, ctx := errgroup.WithContext(ctx)
	g.SetLimit(uploadConcurrency)
	stats := newUploadBatchStats()
	for _, u := range uploads {
		g.Go(func() error {
			defer takeUploadSlot()()
			return tracedUpload(ctx, u.to.up, u.to.bucket, u.key, objectHeaders{}, func() ([]byte, error) {
				return os.ReadFile(u.src)
			}, stats)
		})
	}
	err := g.Wait()
	emitUploadBatch(report, uploadKindPrerenderAsset, stats, err, phaseStart)
	return err
}

type uploadTarget struct {
	up     ArtifactUploader
	bucket string
	class  providerkit.Class
}

func (t uploadTarget) validate() error {
	if t.bucket == "" {
		return fmt.Errorf("this project has objects to publish but no asset bucket is configured; re-run `%s`", providerkit.BootstrapCommand(t.class))
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

func seedTagSnapshot(ctx context.Context, cfg Config, cache *isrConfig, at time.Time) error {
	body, err := json.Marshal(genesisSnapshot(at))
	if err != nil {
		return fmt.Errorf("encode tag snapshot: %w", err)
	}

	for _, target := range snapshotTargets(cfg) {
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
	if isrEntriesAdopted(cfg.objectStores()) {
		return uploadTarget{up: cfg.CacheStoreUploader, bucket: cfg.CacheStoreBucket, class: cfg.Class}
	}
	return uploadTarget{up: cfg.Uploader, bucket: cfg.AssetBucket, class: cfg.Class}
}

func isrEntriesAdopted(stores ObjectStores) bool {
	return stores.CacheStoreBucket != "" && stores.CacheStoreUploader != nil
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
