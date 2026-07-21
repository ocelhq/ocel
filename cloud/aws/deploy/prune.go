// Prune (ticket ocelhq-u8h.8): reclaiming the app-deploy stacks and R2/S3
// objects a Promotion left behind once it falls outside the retention
// window edge.RootTier.DeletePromotionArtifacts enforces. It is a standalone
// command, never run inline on a deploy — an aborted deploy's abandoned
// stack/record is exactly what a later prune sweeps up (see production.go).
//
// ReclaimTargets is pure and unit-tested directly; Prune and Reclaim drive
// the real Pulumi destroy and S3/R2 delete calls and, like Destroy and Run,
// are exercised only by an opt-in run against a live account.
package deploy

import (
	"context"
	"fmt"
	"strings"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	"github.com/ocelhq/ocel/cloud/edge"
)

// PruneTarget is one reclaimed Deployment record's worth of cleanup: the
// app-deploy stack to destroy and the storage prefixes to delete. Derived
// purely from the (app, build id) pair edge.PruneResult.RemovedRecordKeys
// names — Reclaim never needs to re-read the record itself.
type PruneTarget struct {
	App     string
	BuildID string
	// Stack is the app-deploy Pulumi stack this build's Lambdas live in.
	Stack string
	// AssetPrefix is the R2 static-assets prefix uploadStaticAssets wrote
	// this build's output under (ADR 0002).
	AssetPrefix string
	// CachePrefix is the ISR/prerender-config prefix uploadPrerenderAssets
	// wrote this build's cache entries under, in whichever bucket(s) they
	// landed in (entryTarget, at deploy time, may have been either).
	CachePrefix string
}

// removedRecordKeyPrefix is the store's own record-key prefix (recordKey in
// workers/deployments-store/src/store.ts): "record:<app>/<buildId>".
// edge.PruneResult.RemovedRecordKeys carries the store's keys verbatim, so
// ReclaimTargets has to strip it before splitting out app/buildId.
const removedRecordKeyPrefix = "record:"

// ReclaimTargets turns edge.PruneResult.RemovedRecordKeys (the store's own
// "record:<app>/<buildId>" keys) into the concrete stack name and storage
// prefixes each one leaves to reclaim. Pure.
func ReclaimTargets(projectID, env string, removedRecordKeys []string) ([]PruneTarget, error) {
	if len(removedRecordKeys) == 0 {
		return nil, nil
	}
	targets := make([]PruneTarget, 0, len(removedRecordKeys))
	for _, key := range removedRecordKeys {
		trimmed := strings.TrimPrefix(key, removedRecordKeyPrefix)
		app, buildID, ok := strings.Cut(trimmed, "/")
		if !ok || app == "" || buildID == "" {
			return nil, fmt.Errorf("malformed removed record key %q, want %q", key, removedRecordKeyPrefix+"app/buildId")
		}
		targets = append(targets, PruneTarget{
			App:         app,
			BuildID:     buildID,
			Stack:       AppDeployStackName(projectID, app, buildID),
			AssetPrefix: appAssetR2Prefix(projectID, app, buildID),
			CachePrefix: appAssetPrefixFor(env, projectID, app, buildID),
		})
	}
	return targets, nil
}

// PrefixDeleter is the subset of the S3 client Reclaim needs to sweep a
// build's objects: list what a prefix holds and batch-delete it. The
// aws-sdk-go-v2 S3 client (and R2, which speaks the same API) satisfies it;
// tests substitute a fake.
type PrefixDeleter interface {
	ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, optFns ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, optFns ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
}

// deletePrefix removes every object under prefix in bucket, paging through
// ListObjectsV2 and batch-deleting up to 1000 keys per DeleteObjects call
// (the API's own limit). A bucket left unset (no adopted cache store, or a
// prefix that was never written to this bucket) is a deliberate no-op rather
// than an error.
func deletePrefix(ctx context.Context, deleter PrefixDeleter, bucket, prefix string) error {
	if bucket == "" || deleter == nil {
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

// asPrefixDeleter recovers the PrefixDeleter capability an ArtifactUploader
// carries at runtime: the real aws-sdk-go-v2 S3 client (and R2's compatible
// client) always implements both, so this only ever fails a fake configured
// with just the narrower interface — the same capability-check pattern
// cfg.Edge.(edge.RootTier) already uses.
func asPrefixDeleter(up ArtifactUploader) PrefixDeleter {
	d, _ := up.(PrefixDeleter)
	return d
}

// Reclaim destroys one Promotion's collected app-deploy stacks and deletes
// the R2/S3 objects they published: the static-assets prefix from the
// adopted cache store, and the ISR/prerender prefix from both the asset
// bucket (fetch-cache entries always land there) and the adopted cache store
// (route entries may have). Deleting a prefix nothing was ever written to is
// a no-op, so trying both buckets unconditionally is safe. Performs the real
// Pulumi destroy and S3/R2 calls; not exercised by unit tests, like Destroy.
func Reclaim(ctx context.Context, cfg Config, targets []PruneTarget, progress, log func(string)) error {
	for _, t := range targets {
		if progress != nil {
			progress(fmt.Sprintf("Reclaiming %s build %s", t.App, t.BuildID))
		}
		if err := Destroy(ctx, TeardownConfig{
			Region:      cfg.Region,
			BackendURL:  cfg.BackendURL,
			Passphrase:  cfg.Passphrase,
			ProjectName: cfg.ProjectName,
			StackName:   t.Stack,
			Pulumi:      cfg.Pulumi,
		}, progress, log); err != nil {
			return fmt.Errorf("destroy app-deploy stack %s: %w", t.Stack, err)
		}

		if err := deletePrefix(ctx, asPrefixDeleter(cfg.CacheStoreUploader), cfg.CacheStoreBucket, t.AssetPrefix); err != nil {
			return err
		}
		if err := deletePrefix(ctx, asPrefixDeleter(cfg.CacheStoreUploader), cfg.CacheStoreBucket, t.CachePrefix); err != nil {
			return err
		}
		if err := deletePrefix(ctx, asPrefixDeleter(cfg.Uploader), cfg.AssetBucket, t.CachePrefix); err != nil {
			return err
		}
	}
	return nil
}

// Prune reclaims a production project's old Deployments (ADR 0001): tier's
// own DeletePromotionArtifacts enforces the keepN-deep retention window
// (always pinning the active Promotion) and deletes the store records, then
// Reclaim sweeps up what those records named — the app-deploy stacks and
// R2/S3 objects. It backs `ocel deployments prune` and is never run inline on
// a deploy.
func Prune(ctx context.Context, tier edge.RootTier, state edge.RootTierState, cfg Config, projectID string, keepN int, progress, log func(string)) (edge.PruneResult, error) {
	result, err := tier.DeletePromotionArtifacts(ctx, state, keepN)
	if err != nil {
		return edge.PruneResult{}, fmt.Errorf("delete promotion artifacts: %w", err)
	}

	targets, err := ReclaimTargets(projectID, cfg.Env, result.RemovedRecordKeys)
	if err != nil {
		return result, err
	}
	if err := Reclaim(ctx, cfg, targets, progress, log); err != nil {
		return result, err
	}
	return result, nil
}
