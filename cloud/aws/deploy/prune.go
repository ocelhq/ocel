// Prune (ticket ocelhq-u8h.8): reclaiming the app-deploy stacks and R2/S3
// objects a Promotion left behind once it falls outside the retention
// window edge.RootStack.DeletePromotionArtifacts enforces. It is a standalone
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
// purely from the (app, Deployment identity) pair
// edge.PruneResult.RemovedRecordKeys names, and from which of those builds no
// surviving record still shares — Reclaim never needs to re-read the record
// itself.
//
// The storage prefixes are build-keyed, so each is empty whenever a surviving
// Deployment still serves from it: there is nothing to reclaim, only a stack to
// destroy. AssetPrefix, ImageConfigKey and EdgePrefix carry no environment and
// so are shared with every pointer's Deployments of the build; CachePrefix
// carries the environment segment and so is shared only with the pruned
// pointer's own.
type PruneTarget struct {
	App      string
	Identity DeploymentIdentity
	// Stack is the app-deploy Pulumi stack this Deployment's Lambdas live in,
	// named by the identity — the one target here that is not build-keyed, and
	// so the one always reclaimed.
	Stack string
	// AssetPrefix is the R2 static-assets prefix uploadStaticAssets wrote
	// this build's output under (ADR 0002).
	AssetPrefix string
	// ImageConfigKey is the asset-bucket key uploadStaticAssets wrote this
	// build's compiled image config at — a single object outside the assets
	// prefix, so it is swept by its own full key rather than with the rest.
	ImageConfigKey string
	// CachePrefix is the ISR/prerender-config prefix uploadPrerenderAssets
	// wrote this build's cache entries under, in whichever bucket(s) they
	// landed in (entryTarget, at deploy time, may have been either).
	CachePrefix string
	// EdgePrefix is the R2 prefix uploadEdgeBundles wrote this build's edge
	// bundle under (ADR 0002).
	EdgePrefix string
}

// removedRecordKeyPrefix is the store's own record-key prefix (recordKey in
// workers/deployments-store/src/store.ts): "record:<app>/<identity>".
// edge.PruneResult.RemovedRecordKeys carries the store's keys verbatim, so a
// reader has to strip it before splitting out the app and the identity.
const removedRecordKeyPrefix = "record:"

// splitRecordKey splits one store record key into the app and Deployment
// identity it names, reporting ok=false for anything not shaped like one. The
// single place that knows the key's layout — removed and surviving keys alike.
// Pure.
func splitRecordKey(key string) (app string, id DeploymentIdentity, ok bool) {
	app, rendered, split := strings.Cut(strings.TrimPrefix(key, removedRecordKeyPrefix), "/")
	if !split || app == "" {
		return "", DeploymentIdentity{}, false
	}
	id, err := ParseDeploymentIdentity(rendered)
	if err != nil {
		return "", DeploymentIdentity{}, false
	}
	return app, id, true
}

// ReclaimTargets turns edge.PruneResult's removed and surviving record keys
// (the store's own "record:<app>/<identity>" keys) into the concrete production
// stack name each removed Deployment leaves to reclaim, and the storage prefixes
// it leaves only where no surviving Deployment shares its build — project-wide
// for the env-less prefixes, within the pruned pointer for the env-scoped one.
// Pure.
func ReclaimTargets(slug, env string, removedRecordKeys, survivingRecordKeys, survivingPointerRecordKeys []string) ([]PruneTarget, error) {
	return reclaimTargets(slug, env, removedRecordKeys, survivingRecordKeys, survivingPointerRecordKeys, func(app string, id DeploymentIdentity) string {
		return AppDeployStackName(slug, app, id)
	})
}

// PreviewReclaimTargets is ReclaimTargets' preview counterpart: each build's
// app-deploy stack is the pointer-scoped PreviewAppDeployStackName rather than
// the production one, so `preview rm`/`preview prune` reclaim exactly the
// preview's stacks. The storage prefixes are keyed the same way (the asset
// prefix carries no env; the cache prefix carries the preview env segment). Pure.
func PreviewReclaimTargets(slug, pointer, env string, removedRecordKeys, survivingRecordKeys, survivingPointerRecordKeys []string) ([]PruneTarget, error) {
	return reclaimTargets(slug, env, removedRecordKeys, survivingRecordKeys, survivingPointerRecordKeys, func(app string, id DeploymentIdentity) string {
		return PreviewAppDeployStackName(slug, pointer, app, id)
	})
}

// reclaimTargets is the shared core: it splits every removed record key into the
// app and Deployment identity it names and the storage prefixes to delete —
// those keyed by the identity's build id, since that is what the uploads were
// keyed by, and so only for a build no relevant surviving record still names —
// deferring only the app-deploy stack name to stackFor so production and preview
// differ in exactly that one axis. Pure.
func reclaimTargets(slug, env string, removedRecordKeys, survivingRecordKeys, survivingPointerRecordKeys []string, stackFor func(app string, id DeploymentIdentity) string) ([]PruneTarget, error) {
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

// appBuild is one app's build — what the static assets, ISR entries and edge
// bundle are keyed by, and what a rotation's several Deployments share.
type appBuild struct{ app, buildID string }

// servedBuilds is the set of builds the records surviving a prune still serve
// from, so their storage survives with them. A key that doesn't parse names no
// build this reclaim could match and is skipped. Pure.
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
// than an error, as is an unset prefix — a PruneTarget leaves its storage
// prefixes empty when a surviving Deployment still serves that build, and an
// empty prefix would otherwise match the entire bucket.
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

// asPrefixDeleter recovers the PrefixDeleter capability an ArtifactUploader
// carries at runtime: the real aws-sdk-go-v2 S3 client (and R2's compatible
// client) always implements both, so this only ever fails a fake configured
// with just the narrower interface — the same capability-check pattern
// cfg.Edge.(edge.RootStack) already uses.
func asPrefixDeleter(up ArtifactUploader) PrefixDeleter {
	d, _ := up.(PrefixDeleter)
	return d
}

// Reclaim destroys one Promotion's collected app-deploy stacks and deletes
// the R2/S3 objects they published: the edge-bundle prefix from the adopted
// cache store, the static-assets prefix from both halves of the asset plane
// (uploadStaticAssets publishes it to both), the image config from the asset
// bucket alone, and the ISR/prerender prefix from both the asset bucket
// (fetch-cache entries always land there) and the adopted cache store (route
// entries may have). Deleting a prefix nothing was ever written to is a no-op,
// so trying both buckets unconditionally is safe. Performs the real Pulumi
// destroy and S3/R2 calls; not exercised by unit tests, like Destroy.
func Reclaim(ctx context.Context, cfg Config, targets []PruneTarget, progress, log func(string)) error {
	for _, t := range targets {
		if progress != nil {
			progress(fmt.Sprintf("Reclaiming %s deployment %s", t.App, t.Identity))
		}
		if err := Destroy(ctx, teardownConfig(cfg, t.Stack), progress, log); err != nil {
			return fmt.Errorf("destroy app-deploy stack %s: %w", t.Stack, err)
		}

		if err := deletePrefix(ctx, asPrefixDeleter(cfg.CacheStoreUploader), cfg.CacheStoreBucket, t.AssetPrefix); err != nil {
			return err
		}
		if err := deletePrefix(ctx, asPrefixDeleter(cfg.CacheStoreUploader), cfg.CacheStoreBucket, t.CachePrefix); err != nil {
			return err
		}
		if err := deletePrefix(ctx, asPrefixDeleter(cfg.CacheStoreUploader), cfg.CacheStoreBucket, t.EdgePrefix); err != nil {
			return err
		}
		if err := deletePrefix(ctx, asPrefixDeleter(cfg.Uploader), cfg.AssetBucket, t.AssetPrefix); err != nil {
			return err
		}
		if err := deletePrefix(ctx, asPrefixDeleter(cfg.Uploader), cfg.AssetBucket, t.ImageConfigKey); err != nil {
			return err
		}
		if err := deletePrefix(ctx, asPrefixDeleter(cfg.Uploader), cfg.AssetBucket, t.CachePrefix); err != nil {
			return err
		}
	}
	return nil
}

// Prune reclaims a project's old Deployments (ADR 0001): stack's own
// DeletePromotionArtifacts enforces the keepN-deep retention window for the
// pointer (always pinning its active Promotion) and deletes the store records,
// then Reclaim sweeps up what those records named — the app-deploy stacks and
// R2/S3 objects. It backs `ocel deployments prune` (production, empty pointer)
// and `ocel preview prune --name` (a persistent preview's pointer), and is
// never run inline on a deploy. The pointer selects the substrate's stack
// naming: an empty pointer reclaims production stacks, a named one reclaims that
// preview pointer's pointer-scoped stacks.
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

// reclaimTargetsFor picks the substrate-correct reclaim targets: production
// stacks for the empty (reserved default) pointer, pointer-scoped preview stacks
// for any named pointer. Pure.
func reclaimTargetsFor(slug, pointer, env string, removedRecordKeys, survivingRecordKeys, survivingPointerRecordKeys []string) ([]PruneTarget, error) {
	if pointer == "" {
		return ReclaimTargets(slug, env, removedRecordKeys, survivingRecordKeys, survivingPointerRecordKeys)
	}
	return PreviewReclaimTargets(slug, pointer, env, removedRecordKeys, survivingRecordKeys, survivingPointerRecordKeys)
}
