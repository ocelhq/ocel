package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/ocelhq/ocel/pkg/naming"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type PruneTarget struct {
	App            string
	Identity       Identity
	Stack          naming.StackName
	AssetPrefix    string
	ImageConfigKey string
	CachePrefix    string
	EdgePrefix     string
	FunctionPrefix string
}

const removedRecordKeyPrefix = "record:"

func splitRecordKey(key string) (app string, id Identity, ok bool) {
	app, rendered, split := strings.Cut(strings.TrimPrefix(key, removedRecordKeyPrefix), "/")
	if !split || app == "" {
		return "", Identity{}, false
	}
	id, err := ParseIdentity(rendered)
	if err != nil {
		return "", Identity{}, false
	}
	return app, id, true
}

func ReclaimTargets(slug, env string, removedRecordKeys, survivingRecordKeys, survivingPointerRecordKeys []string) ([]PruneTarget, error) {
	if len(removedRecordKeys) == 0 {
		return nil, nil
	}
	sharedElsewhere := servedReleases(survivingRecordKeys)
	servedHere := servedReleases(survivingPointerRecordKeys)
	targets := make([]PruneTarget, 0, len(removedRecordKeys))
	for _, key := range removedRecordKeys {
		app, id, ok := splitRecordKey(key)
		if !ok {
			return nil, fmt.Errorf("malformed removed record key %q, want %q", key, removedRecordKeyPrefix+"app/identity")
		}
		release := releaseOf(id)
		coord := storageCoordinate(env, slug, app, release)
		target := PruneTarget{App: app, Identity: id, Stack: naming.AppStack(env, app, release)}
		released := appRelease{app, release}
		if !sharedElsewhere[released] {
			target.AssetPrefix = appAssetPrefix(coord)
			target.ImageConfigKey = coord.ImageConfigKey()
			target.EdgePrefix = appEdgePrefix(coord)
			target.FunctionPrefix = functionArtifactPrefix(coord)
		}
		if !servedHere[released] {
			target.CachePrefix = isrPrefixOf(coord)
		}
		targets = append(targets, target)
	}
	return targets, nil
}

type appRelease struct {
	app     string
	release naming.Release
}

func servedReleases(survivingRecordKeys []string) map[appRelease]bool {
	served := make(map[appRelease]bool, len(survivingRecordKeys))
	for _, key := range survivingRecordKeys {
		app, id, ok := splitRecordKey(key)
		if !ok {
			continue
		}
		served[appRelease{app, releaseOf(id)}] = true
	}
	return served
}

type PrefixDeleter interface {
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

func deletePrefix(ctx context.Context, deleter PrefixDeleter, bucket, prefix string) error {
	if bucket == "" || prefix == "" || deleter == nil {
		return nil
	}
	var token *string
	for {
		out, err := deleter.ListObjectsV2(ctx, &s3.ListObjectsV2Input{
			Bucket:            aws.String(bucket),
			Prefix:            aws.String(prefix),
			ContinuationToken: token,
		})
		if err != nil {
			return fmt.Errorf("list %s/%s: %w", bucket, prefix, err)
		}
		if len(out.Contents) > 0 {
			ids := make([]s3types.ObjectIdentifier, len(out.Contents))
			for i, obj := range out.Contents {
				ids[i] = s3types.ObjectIdentifier{Key: obj.Key}
			}
			if _, err := deleter.DeleteObjects(ctx, &s3.DeleteObjectsInput{
				Bucket: aws.String(bucket),
				Delete: &s3types.Delete{Objects: ids},
			}); err != nil {
				return fmt.Errorf("delete %s/%s: %w", bucket, prefix, err)
			}
		}
		if out.IsTruncated == nil || !*out.IsTruncated {
			return nil
		}
		token = out.NextContinuationToken
	}
}

func asPrefixDeleter(up ArtifactUploader) PrefixDeleter {
	d, _ := up.(PrefixDeleter)
	return d
}

func Reclaim(ctx context.Context, cfg Config, targets []PruneTarget, progress, log func(string)) error {
	progress, log = serializeReports(progress, log)
	return errors.Join(runBounded(teardownConcurrency, targets, func(t PruneTarget) error {
		if progress != nil {
			progress(fmt.Sprintf("Reclaiming %s deployment %s", t.App, t.Identity))
		}
		return reclaimTarget(ctx, cfg, t, progress, log)
	})...)
}

type prefixTarget struct {
	deleter PrefixDeleter
	bucket  string
	prefix  string
}

func reclaimTarget(ctx context.Context, cfg Config, t PruneTarget, progress, log func(string)) error {
	if err := Destroy(ctx, teardownConfig(cfg, t.Stack), progress, log); err != nil {
		return fmt.Errorf("destroy app-deploy stack %s: %w", t.Stack, err)
	}

	cacheStore, account := asPrefixDeleter(cfg.CacheStoreUploader), asPrefixDeleter(cfg.Uploader)
	reclaims := make([]func() error, 0, 8)
	for _, d := range []prefixTarget{
		{cacheStore, cfg.CacheStoreBucket, t.AssetPrefix},
		{cacheStore, cfg.CacheStoreBucket, t.CachePrefix},
		{cacheStore, cfg.CacheStoreBucket, t.EdgePrefix},
		{account, cfg.AssetBucket, t.AssetPrefix},
		{account, cfg.AssetBucket, t.ImageConfigKey},
		{account, cfg.AssetBucket, t.CachePrefix},
		{account, cfg.ArtifactBucket, t.FunctionPrefix},
	} {
		reclaims = append(reclaims, func() error { return deletePrefix(ctx, d.deleter, d.bucket, d.prefix) })
	}
	if t.CachePrefix != "" {
		reclaims = append(reclaims, func() error { return retireISRWriter(ctx, cfg, t.CachePrefix) })
	}
	return errors.Join(runBounded(len(reclaims), reclaims, func(run func() error) error { return run() })...)
}

func Prune(ctx context.Context, stack edge.RootStack, state edge.RootStackState, cfg Config, slug string, keepN int, pointer string, progress, log func(string)) (edge.PruneResult, error) {
	result, err := stack.DeletePromotionArtifacts(ctx, state, keepN, pointer)
	if err != nil {
		return edge.PruneResult{}, fmt.Errorf("delete promotion artifacts: %w", err)
	}

	targets, err := ReclaimTargets(slug, cfg.Env, result.RemovedRecordKeys, result.SurvivingRecordKeys, result.SurvivingPointerRecordKeys)
	if err != nil {
		return result, err
	}
	if err := Reclaim(ctx, cfg, targets, progress, log); err != nil {
		return result, err
	}
	return result, nil
}
