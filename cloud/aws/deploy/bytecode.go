package deploy

import (
	"encoding/json"
	"fmt"
	"os"
	"path"
	"strings"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// bytecodeCacheEnv is the deploying process's own switch for whether the
// membrane caches V8 bytecode. Off by default — the feature has AWS-side cost
// (a standing S3 grant, the warm and embed passes) that a project should ask
// for, and only the literal "1" turns it on, mirroring how every other strict
// deploy-time opt-in in this package is spelled.
const bytecodeCacheEnv = "OCEL_BYTECODE_CACHE"

// bytecodeCacheEnabled reports whether this deploy should cache V8 bytecode
// for its nodejs* functions, read from OCEL_BYTECODE_CACHE on the deploying
// process. Off by default; only the literal "1" turns it on.
func bytecodeCacheEnabled() bool {
	return os.Getenv(bytecodeCacheEnv) == "1"
}

// isNodeRuntime reports whether a Lambda runtime string is one the lambdanode
// membrane runs under — the only condition the bytecode feature gates a
// function on. It is a function's own resolved runtime that decides this, not
// its framework: an express or fastify function is exactly as eligible as a
// Next one, since the membrane that reads OCEL_BYTECODE_PREFIX is
// framework-agnostic (see membrane.mts and lambdanode's bootstrap).
func isNodeRuntime(runtime string) bool {
	return strings.HasPrefix(runtime, "nodejs")
}

// bytecodeKeyNamespace is the root segment every bytecode cache object lives
// under in the account-global asset bucket — its own space, deliberately
// apart from appAssetPrefix's build-keyed one. That prefix is what prune
// sweeps on every build (prune.go); a bytecode cache that lived under it would
// be reaped by the very redeploy it exists to speed up. A dedicated root also
// rules out any collision with an ISR or static-asset key, which share the
// same bucket.
//
// It sits FIRST in bytecodeAppNamespace, not after env/slug, so the asset
// bucket's lifecycle rule (bootstrap's expire-bytecode) can select every
// bytecode object with one literal S3 prefix filter. S3 lifecycle filters
// match a literal prefix, not a path segment in the middle of a key, so
// "bytecode/" has to lead. The one string this then must never equal is an
// env segment — env is never a bare user string (envSegment in
// cloud/aws/server/server.go composes it as exactly "prod" or
// "preview-<identity>"), so no project can ever cause a collision by naming
// an environment "bytecode".
const bytecodeKeyNamespace = "bytecode"

// bytecodeAppNamespace is the app-scoped root every one of an app's bytecode
// caches lives under, and exactly what that app's IAM grant is scoped to
// (bytecodePolicy). One wildcard grant here covers every content hash the app
// will ever produce, because the hash sits below this namespace
// (bytecodePrefixFor) rather than inside it — so a code change never needs a
// new grant, only a new object under one already held. The IAM execution role
// is per-app (newFunctionRole/appExecutionRole), which is what this shape is
// chosen to keep true: a single RolePolicy issued once per app, not once per
// deploy or per function.
//
// bytecodeKeyNamespace leads rather than trailing env/slug (see its own doc)
// so the bucket-wide lifecycle rule can filter on one literal prefix; the
// per-app IAM grant stays exactly as narrow either way, since it wildcards
// everything below this whole path regardless of where the fixed segment
// sits within it.
func bytecodeAppNamespace(env, slug, app string) string {
	return path.Join(bytecodeKeyNamespace, env, slug, sanitizeWorkerName(app))
}

// bytecodePrefixFor is one function's own bytecode cache prefix: the app's
// namespace with the function's content hash appended. Two functions whose
// `.func` trees hash identically — the ordinary case for an unchanged
// redeploy — share the same prefix and so the same cache, which is the whole
// point: identical code finds a cache already warm instead of paying to
// rebuild one.
func bytecodePrefixFor(env, slug, app, hash string) string {
	return path.Join(bytecodeAppNamespace(env, slug, app), hash)
}

// bytecodeFunctionConfig is one function's resolved bytecode-cache identity:
// where its compile cache lives. Independent of isrConfig — ISR is a Next-only
// cache with its own build-keyed prefix; bytecode caching reaches any nodejs*
// function and survives across builds by design, so the two must never share
// a carrier or a key space.
type bytecodeFunctionConfig struct {
	Bucket string
	Prefix string
}

// env is what the membrane reads to find its bytecode cache: the bucket under
// its own var, deliberately not OCEL_ISR_BUCKET — that var is unset for any
// app with no ISR cache, which used to be every non-Next app the feature now
// also has to reach.
func (c *bytecodeFunctionConfig) env() map[string]string {
	if c == nil {
		return nil
	}
	return map[string]string{
		"OCEL_BYTECODE_PREFIX": c.Prefix,
		"OCEL_BYTECODE_BUCKET": c.Bucket,
	}
}

// resolveBytecodeFunctionConfig is one function's bytecode-cache identity, or
// nil for a function the feature does not reach: the deploy-wide gate is off,
// or the function's own resolved runtime is not nodejs* (translateFunction's
// default already resolves nodejs24.x for an empty one, so an ordinary
// function is admitted without declaring anything).
//
// hash is the function's own `.func` tree hashed WITHOUT the baked-values
// overlay — bare, in artifactRef's sense — which the caller already computed
// once in uploadFunctionArtifacts (hashArtifactPair) and hands in here rather
// than this function reading the tree a second time. See vars.go's
// renderAppBundle for why the overlay must stay out of it: the overlay is a
// sealed data file (baked.FilePath / live.FilePath) the SDK reads off disk at
// runtime, never a module V8 compiles, and renderAppBundle draws a fresh
// crypto/rand data key on every render, which would otherwise draw a fresh
// hash on every single deploy of an app declaring a `sensitive` variable —
// defeating the one property this cache exists for, that identical code
// redeploys hit an existing cache.
func resolveBytecodeFunctionConfig(cfg Config, slug, app string, fn *deploymentsv1.ManifestFunction, hash string) *bytecodeFunctionConfig {
	if !bytecodeCacheEnabled() || !isNodeRuntime(translateFunction(fn).Runtime) {
		return nil
	}
	return &bytecodeFunctionConfig{
		Bucket: cfg.AssetBucket,
		Prefix: bytecodePrefixFor(cfg.Env, slug, app, hash),
	}
}

// appBytecodeNamespace is the app-scoped bytecode key namespace this app's
// execution role should be granted, or "" when nothing in it qualifies: the
// deploy-wide gate is off, or none of the app's functions resolve a nodejs*
// runtime — the only runtime the membrane's bytecode legs ever activate under.
func appBytecodeNamespace(cfg Config, slug, app string, functions []*deploymentsv1.ManifestFunction) string {
	if !bytecodeCacheEnabled() {
		return ""
	}
	for _, fn := range functions {
		if isNodeRuntime(translateFunction(fn).Runtime) {
			return bytecodeAppNamespace(cfg.Env, slug, app)
		}
	}
	return ""
}

// bytecodePolicy grants a function's role exactly the bytecode-cache access
// the membrane's two legs need on the app's own namespace, and no more:
// GetObject for the read leg (rehydrating an existing cache) and PutObject for
// the warm publish. It is issued as its own RolePolicy rather than folded into
// isrPolicy — bytecode's namespace sits deliberately outside the ISR prefix
// (bytecodeKeyNamespace), so a grant scoped to one must never cover the other,
// and this now applies to every app the feature is on for, Next or not.
func bytecodePolicy(bucket, namespace string) (string, error) {
	doc := map[string]any{
		"Version": "2012-10-17",
		"Statement": []any{
			map[string]any{
				"Effect":   "Allow",
				"Action":   []string{"s3:GetObject", "s3:PutObject"},
				"Resource": fmt.Sprintf("arn:aws:s3:::%s/%s/*", bucket, namespace),
			},
		},
	}
	out, err := json.Marshal(doc)
	if err != nil {
		return "", fmt.Errorf("render bytecode policy: %w", err)
	}
	return string(out), nil
}
