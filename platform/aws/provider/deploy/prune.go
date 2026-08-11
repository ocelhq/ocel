package deploy

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type PruneTarget struct {
	App            string
	Identity       Identity
	Stack          string
	AssetPrefix    string
	ImageConfigKey string
	CachePrefix    string
	EdgePrefix     string
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
	return reclaimTargets(slug, env, removedRecordKeys, survivingRecordKeys, survivingPointerRecordKeys, func(app string, id Identity) string {
		return AppDeployStackName(slug, app, id)
	})
}

func PreviewReclaimTargets(slug, pointer, env string, removedRecordKeys, survivingRecordKeys, survivingPointerRecordKeys []string) ([]PruneTarget, error) {
	return reclaimTargets(slug, env, removedRecordKeys, survivingRecordKeys, survivingPointerRecordKeys, func(app string, id Identity) string {
		return PreviewAppDeployStackName(slug, pointer, app, id)
	})
}

func reclaimTargets(slug, env string, removedRecordKeys, survivingRecordKeys, survivingPointerRecordKeys []string, stackFor func(app string, id Identity) string) ([]PruneTarget, error) {
	if len(removedRecordKeys) == 0 {
		return nil, nil
	}
	sharedElsewhere := servedBuilds(survivingRecordKeys)
	servedHere := servedBuilds(survivingPointerRecordKeys)
	targets := make([]PruneTarget, 0, len(removedRecordKeys))
	for _, key := range removedRecordKeys {
		app, id, ok := splitRecordKey(key)
		if !ok {
			return nil, fmt.Errorf("malformed removed record key %q, want %q", key, removedRecordKeyPrefix+"app/identity")
		}
		target := PruneTarget{App: app, Identity: id, Stack: stackFor(app, id)}
		build := appBuild{app, id.BuildID()}
		if !sharedElsewhere[build] {
			target.AssetPrefix = appAssetR2Prefix(slug, app, id.BuildID())
			target.ImageConfigKey = imageConfigKey(slug, app, id.BuildID())
			target.EdgePrefix = appEdgeR2Prefix(slug, app, id.BuildID())
		}
		if !servedHere[build] {
			target.CachePrefix = appAssetPrefixFor(env, slug, app, id.BuildID())
		}
		targets = append(targets, target)
	}
	return targets, nil
}

type appBuild struct{ app, buildID string }

func servedBuilds(survivingRecordKeys []string) map[appBuild]bool {
	served := make(map[appBuild]bool, len(survivingRecordKeys))
	for _, key := range survivingRecordKeys {
		app, id, ok := splitRecordKey(key)
		if !ok {
			continue
		}
		served[appBuild{app, id.BuildID()}] = true
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
	var errs []error
	for _, t := range targets {
		if progress != nil {
			progress(fmt.Sprintf("Reclaiming %s deployment %s", t.App, t.Identity))
		}
		errs = append(errs, reclaimTarget(ctx, cfg, t, progress, log))
	}
	return errors.Join(errs...)
}

func reclaimTarget(ctx context.Context, cfg Config, t PruneTarget, progress, log func(string)) error {
	if err := Destroy(ctx, teardownConfig(cfg, t.Stack), progress, log); err != nil {
		return fmt.Errorf("destroy app-deploy stack %s: %w", t.Stack, err)
	}

	var errs []error
	for _, p := range []string{t.AssetPrefix, t.CachePrefix, t.EdgePrefix} {
		errs = append(errs, deletePrefix(ctx, asPrefixDeleter(cfg.CacheStoreUploader), cfg.CacheStoreBucket, p))
	}
	for _, p := range []string{t.AssetPrefix, t.ImageConfigKey, t.CachePrefix} {
		errs = append(errs, deletePrefix(ctx, asPrefixDeleter(cfg.Uploader), cfg.AssetBucket, p))
	}
	if t.CachePrefix != "" {
		errs = append(errs, retireISRWriter(ctx, cfg, t.CachePrefix))
	}
	return errors.Join(errs...)
}

func Prune(ctx context.Context, stack edge.RootStack, state edge.RootStackState, cfg Config, slug string, keepN int, pointer string, progress, log func(string)) (edge.PruneResult, error) {
	result, err := stack.DeletePromotionArtifacts(ctx, state, keepN, pointer)
	if err != nil {
		return edge.PruneResult{}, fmt.Errorf("delete promotion artifacts: %w", err)
	}

	targets, err := reclaimTargetsFor(slug, pointer, cfg.Env, result.RemovedRecordKeys, result.SurvivingRecordKeys, result.SurvivingPointerRecordKeys)
	if err != nil {
		return result, err
	}
	if err := Reclaim(ctx, cfg, targets, progress, log); err != nil {
		return result, err
	}
	return result, nil
}

func reclaimTargetsFor(slug, pointer, env string, removedRecordKeys, survivingRecordKeys, survivingPointerRecordKeys []string) ([]PruneTarget, error) {
	if pointer == "" {
		return ReclaimTargets(slug, env, removedRecordKeys, survivingRecordKeys, survivingPointerRecordKeys)
	}
	return PreviewReclaimTargets(slug, pointer, env, removedRecordKeys, survivingRecordKeys, survivingPointerRecordKeys)
}
