package deploy

import (
	"archive/zip"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"golang.org/x/sync/errgroup"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// embedCacheCeiling is the largest compile cache worth embedding.
//
// THIS IS THE DIAL. Embedding trades an S3 GET during INIT (~50-150ms, inside
// the startup budget) for a bigger deployment package, which makes Lambda's own
// download-and-unzip slower — also cold start, just unbilled and unmeasurable
// from inside the sandbox. Past some cache size the trade goes net-negative,
// and only a benchmark can say where. Until one does, this is a deliberately
// conservative guess: move this constant, not the surrounding logic.
const embedCacheCeiling = 32 << 20

// embedUnzippedCeiling is legality rather than judgement. AWS caps a function
// at 250 MB unzipped *including its layers*, and the limit is not raisable, so
// a package over it cannot be deployed at all. Reserving the remainder for the
// membrane layer is what buys this side out of a GetLayerVersion call — whose
// CodeSize is the compressed size anyway, and so cannot answer the question.
const embedUnzippedCeiling = 200 << 20

// embedConcurrency bounds how many bundles are re-packaged at once, and is
// appConcurrency for the same reason warmConcurrency is: the rationed resource
// is the account's Lambda control-plane and concurrency budget, not this
// machine. Each worker holds two temp files and one zip window, so the local
// cost of the number is bounded by the streaming, not by the number.
const embedConcurrency = appConcurrency

// embedPassDeadline caps what embedding can add to a deploy. Like the warm
// pass it runs before the promote, so every second here is a second the
// previous Deployment keeps serving — and unlike warming, nothing is lost when
// it is cut off: a bundle that does not get embedded fetches its cache from S3
// exactly as it does today.
const embedPassDeadline = 3 * time.Minute

// embedUpdatePoll is how often the pass asks whether an UpdateFunctionCode has
// settled. Invoking a function whose LastUpdateStatus is still InProgress can
// fail, and the verify invoke follows immediately.
const embedUpdatePoll = 500 * time.Millisecond

// embedUpdateSettle is how long one code update is given to reach Successful.
// UpdateFunctionCode returns as soon as the request is accepted and the
// function lands on the new package asynchronously — usually within a second or
// two — so this is generous enough that timing out means something is actually
// wrong rather than merely slow.
//
// It is also what the pass checks the *remaining* deadline against before
// issuing an update at all. That ordering is the point: once the update is
// accepted the function is already moving, and refusing to start one this pass
// cannot afford to wait out is the only way "left on its original package"
// stays a true thing to say.
const embedUpdateSettle = 45 * time.Second

// ObjectGetter is the subset of the S3 client the embed pass needs: one read.
// It fetches two objects — the published cache tarball and the function's
// original deployment package. The aws-sdk-go-v2 S3 client satisfies it, so
// nothing adapts it at the call site; tests substitute a fake and drive every
// branch with no AWS client, config or credentials in reach.
type ObjectGetter interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

// FunctionCodeUpdater is the subset of the Lambda client the embed pass needs:
// repoint a function at a new package, and read back whether that update has
// settled. Kept separate from FunctionInvoker because the two are needed by
// different passes and a fake for one should not have to answer for the other.
type FunctionCodeUpdater interface {
	UpdateFunctionCode(ctx context.Context, in *lambda.UpdateFunctionCodeInput, optFns ...func(*lambda.Options)) (*lambda.UpdateFunctionCodeOutput, error)
	GetFunctionConfiguration(ctx context.Context, in *lambda.GetFunctionConfigurationInput, optFns ...func(*lambda.Options)) (*lambda.GetFunctionConfigurationOutput, error)
}

// embedTarget is one bundle's whole embed job: where its published cache is,
// where its deployed package is, and how big that package unzips to. Every
// field is resolved before the pass runs, so the pass itself touches no
// manifest, no config and no local disk beyond its own temp files.
type embedTarget struct {
	App          string
	LogicalName  string
	FunctionName string

	// Artifact is the package the warm pass just proved, and the base of the
	// key the merged one lands at.
	Artifact artifactRef
	// CacheBucket and CacheKey address the tarball the membrane published. The
	// key is the membrane's own, echoed back in its warm summary: it embeds
	// node's full version, which only the sandbox ever learns.
	CacheBucket string
	CacheKey    string
	// TreeBytes is what Artifact unzips to, measured from the local `.func`
	// tree it was built from rather than by reading the object back.
	TreeBytes int64
}

// embedBytecodeCaches embeds each warmed bundle's published compile cache into
// its own deployment package, so a cold start reads it from /var/task instead
// of fetching it from S3. It runs after the warm pass — which is what produced
// the caches and told this side their keys — and before the promote, so no
// traffic ever reaches a function mid-update.
//
// It is opt-in and best-effort in both directions: the S3 path stays exactly as
// it is, and a function this pass cannot re-package keeps the artifact it was
// deployed with.
func embedBytecodeCaches(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, artifacts map[string]artifactRef, warmed []warmResult, log func(string)) {
	if log == nil {
		log = func(string) {}
	}
	if !bytecodeEmbedRequested() {
		return
	}
	if !bytecodeEmbedEnabled() {
		log(fmt.Sprintf("ocel: %s=1 has nothing to embed while %s=0; not embedding", bytecodeEmbedEnv, bytecodeCacheEnv))
		return
	}
	// Named rather than silent: a caller that wires no clients is a deploy path
	// this feature was never plumbed into, and the user who set the flag and got
	// nothing has no other way to find that out.
	if missing := missingEmbedClients(cfg); missing != "" {
		log(fmt.Sprintf("ocel: %s=1 but this deploy has no %s; not embedding", bytecodeEmbedEnv, missing))
		return
	}
	caches, err := appCaches(cfg, manifest)
	if err != nil {
		log(fmt.Sprintf("ocel: could not work out which bundles to embed: %v", err))
		return
	}
	embedPass{
		objects:  cfg.Getter,
		uploader: cfg.Uploader,
		code:     cfg.CodeUpdater,
		invoker:  cfg.Invoker,
		targets:  embedTargets(cfg, manifest, caches, artifacts, warmed, log),
		budget:   embedPassDeadline,
		settle:   embedUpdateSettle,
		log:      log,
	}.run(ctx)
}

// missingEmbedClients names the clients the pass needs and this Config does not
// carry, or "" when it carries all four. It names them rather than counting
// them: the whole point of reporting is that the answer is actionable.
func missingEmbedClients(cfg Config) string {
	var missing []string
	for _, c := range []struct {
		name    string
		present bool
	}{
		{"object getter", cfg.Getter != nil},
		{"code updater", cfg.CodeUpdater != nil},
		{"artifact uploader", cfg.Uploader != nil},
		{"function invoker", cfg.Invoker != nil},
	} {
		if !c.present {
			missing = append(missing, c.name)
		}
	}
	return strings.Join(missing, ", ")
}

// embedTargets are the bundles this pass can act on: those the warm pass left
// holding a cache, whose app keeps one, and whose artifact this deploy uploaded.
// A bundle missing any of the three is dropped silently — the warm pass has
// already reported why it has no cache, and repeating it here would double
// every line of a deploy that warmed nothing.
//
// Nothing else is filtered here. A bundle that answered from an embedded copy
// is still a target: the merge refuses a package that already holds the entry,
// which is a named line, whereas dropping it here would be the one outcome
// neither pass ever reports.
func embedTargets(cfg Config, manifest *deploymentsv1.Manifest, caches map[string]*isrConfig, artifacts map[string]artifactRef, warmed []warmResult, log func(string)) []embedTarget {
	dirs := map[string]string{}
	for _, fn := range manifest.GetFunctions() {
		dirs[fn.GetLogicalName()] = artifactArchivePath(cfg.ArtifactRoot, fn.GetArtifactPath())
	}
	var targets []embedTarget
	for _, result := range warmed {
		logical := result.Target.LogicalName
		cache := caches[result.Target.App]
		artifact := artifacts[logical]
		if cache == nil || artifact.Key == "" || result.Reply.Key == "" {
			continue
		}
		// Measured, not estimated: the hard ceiling is what stands between this
		// pass and a package Lambda refuses, so a tree it cannot size is a tree
		// it must not embed into.
		size, err := unzippedTreeBytes(dirs[logical])
		if err != nil {
			log(fmt.Sprintf("  %s app=%s  could not size the package: %v; not embedded", logical, result.Target.App, err))
			continue
		}
		targets = append(targets, embedTarget{
			App:          result.Target.App,
			LogicalName:  logical,
			FunctionName: result.Target.FunctionName,
			Artifact:     artifact,
			CacheBucket:  cache.Bucket,
			CacheKey:     result.Reply.Key,
			TreeBytes:    size,
		})
	}
	return targets
}

// unzippedTreeBytes is what a `.func` tree occupies once Lambda has unzipped
// it: the sum of its regular files, which is the number the 250 MB limit is
// measured against. The overlay is not counted — it is a handful of kilobytes
// against a ceiling in hundreds of megabytes, and reaching it here would mean
// threading every app's sealed bundle through the pass to say so.
func unzippedTreeBytes(dir string) (int64, error) {
	if dir == "" {
		return 0, fmt.Errorf("no artifact directory")
	}
	rels, err := walkRegularFiles(dir)
	if err != nil {
		return 0, err
	}
	var total int64
	for _, rel := range rels {
		info, err := os.Lstat(filepath.Join(dir, rel))
		if err != nil {
			return 0, err
		}
		total += info.Size()
	}
	return total, nil
}

// embedPass re-packages each target with its compile cache inside it and moves
// the function onto the result. Everything it depends on is a field, so the
// whole pass is exercisable without AWS.
type embedPass struct {
	objects  ObjectGetter
	uploader ArtifactUploader
	code     FunctionCodeUpdater
	invoker  FunctionInvoker
	targets  []embedTarget
	budget   time.Duration
	settle   time.Duration
	log      func(string)
}

// run embeds every target, or says why it did not. Nothing here can fail a
// deploy: a bundle this pass gives up on is still deployed, still warmed, and
// still fetches its cache from S3 on a cold start — which is the behaviour of
// every deploy that never opted in.
func (p embedPass) run(ctx context.Context) {
	if len(p.targets) == 0 || p.objects == nil || p.code == nil {
		return
	}
	p.log(fmt.Sprintf("ocel: embedding compile caches into %s (%d at a time)", plural(len(p.targets), "bundle", "bundles"), embedConcurrency))

	ctx, cancel := context.WithTimeout(ctx, p.budget)
	defer cancel()

	start := time.Now()
	var (
		mu       sync.Mutex
		embedded int
		skipped  = make([]bool, len(p.targets))
	)
	var g errgroup.Group
	g.SetLimit(embedConcurrency)
	for i, target := range p.targets {
		g.Go(func() error {
			// Checked up front for the same reason the warm pass checks it: a
			// target the deadline never admitted has not failed, and reporting
			// it as a cancelled GET would hide that the pass ran out of time.
			if ctx.Err() != nil {
				skipped[i] = true
				return nil
			}
			at := time.Now()
			outcome, ok := p.embedOne(ctx, target)
			mu.Lock()
			defer mu.Unlock()
			if ok {
				embedded++
			}
			p.log(fmt.Sprintf("  %s app=%s  %s  %.1fs", target.LogicalName, target.App, outcome, time.Since(at).Seconds()))
			return nil
		})
	}
	_ = g.Wait() // embedOne returns no error, so Wait cannot.

	for i, target := range p.targets {
		if skipped[i] {
			p.log(fmt.Sprintf("  %s app=%s  the embed pass ran out of time; not embedded", target.LogicalName, target.App))
		}
	}
	p.log(fmt.Sprintf("ocel: embedded %d/%d compile caches in %.0fs", embedded, len(p.targets), time.Since(start).Seconds()))
}

// embedOne carries one bundle through the whole sequence and reduces it to the
// line the deploy reports it under. Every return before the last leaves the
// function on the artifact it was deployed with.
func (p embedPass) embedOne(ctx context.Context, target embedTarget) (string, bool) {
	entry, err := embeddedTarPath(target.CacheKey)
	if err != nil {
		return fmt.Sprintf("%v; not embedded", err), false
	}
	work, err := os.MkdirTemp("", "ocel-embed-")
	if err != nil {
		return fmt.Sprintf("no working directory: %v; not embedded", err), false
	}
	defer os.RemoveAll(work)

	tarPath := filepath.Join(work, "cache.tar")
	tarBytes, digest, err := p.fetchCacheTar(ctx, target, tarPath)
	if err != nil {
		return fmt.Sprintf("could not read the published cache: %v; not embedded", err), false
	}
	if ok, why := embedGate(target.TreeBytes, tarBytes); !ok {
		return why, false
	}

	zipPath := filepath.Join(work, "artifact.zip")
	if err := p.fetchObject(ctx, target.Artifact.Bucket, target.Artifact.Key, zipPath, embedUnzippedCeiling); err != nil {
		return fmt.Sprintf("could not read the deployed package: %v; not embedded", err), false
	}
	merged := filepath.Join(work, "merged.zip")
	if err := mergeEmbeddedTar(merged, zipPath, tarPath, entry); err != nil {
		return fmt.Sprintf("could not repackage: %v; not embedded", err), false
	}

	key, err := embeddedArtifactKey(target.Artifact.Key, digest)
	if err != nil {
		return fmt.Sprintf("%v; not embedded", err), false
	}
	if err := p.putFile(ctx, target.Artifact.Bucket, key, merged); err != nil {
		return fmt.Sprintf("could not upload the repackaged bundle: %v; not embedded", err), false
	}
	if err := p.updateCode(ctx, target, key); err != nil {
		if errors.Is(err, errUpdateUnsettled) {
			// The one failure that does not leave the function where it was, so
			// it may not be reported as though it did. It is also deliberately
			// not verified: the update is in flight and an invoke against a
			// function mid-update can fail, which would turn a harmless late
			// landing into a loud one. Harmless because the merged package is
			// byte-for-byte the warmed one plus a file, and a membrane that
			// finds no matching tar simply fetches from S3 as it does today.
			return fmt.Sprintf("embedded %s, but %v; the function is moving onto %s unverified", entry, err, key), false
		}
		return fmt.Sprintf("%v; left on its original package", err), false
	}

	// The verify is the only thing that distinguishes an embed that landed from
	// one that silently did not — the membrane answers already-cached either
	// way. It cannot fail the deploy: S3 still holds the same cache, so the
	// worst case here is exactly the cold start of not having embedded at all.
	reply, failure := invokeWarm(ctx, p.invoker, target.FunctionName)
	switch {
	case failure != "":
		return fmt.Sprintf("embedded %s, but could not verify it: %s", entry, failure), false
	case reply.Source != warmSourceEmbedded:
		return fmt.Sprintf("embedded %s, but the function still answered from %q; it will keep fetching from S3", entry, reply.Source), false
	}
	return fmt.Sprintf("embedded %s (%.1f MiB) at %s", entry, float64(tarBytes)/(1<<20), key), true
}

// embedGate decides whether a bundle's cache may be embedded, from the two
// sizes alone: what the package already unzips to, and what the cache adds.
//
// Two bounds, for two different reasons. The first is legality — past it AWS
// rejects the package and the function cannot be updated at all. The second is
// judgement about whether embedding still pays; see embedCacheCeiling.
func embedGate(treeBytes, tarBytes int64) (bool, string) {
	if treeBytes+tarBytes > embedUnzippedCeiling {
		return false, fmt.Sprintf("the package would unzip to %.1f MiB, over the %d MiB limit; not embedded",
			float64(treeBytes+tarBytes)/(1<<20), embedUnzippedCeiling>>20)
	}
	if tarBytes > embedCacheCeiling {
		return false, fmt.Sprintf("the cache is %.1f MiB, over the %d MiB embed ceiling; not embedded",
			float64(tarBytes)/(1<<20), embedCacheCeiling>>20)
	}
	return true, ""
}

// embeddedTarPath is where a published cache lives inside the deployment
// package: `.ocel/bytecode/node<version>-<arch>.tar`, alongside the overlay
// files that already share that namespace.
//
// It is derived from the key the membrane composed rather than assembled here,
// because the version in that key is node's actual version, which only the
// sandbox learns. The membrane derives the same path the same way on the way
// in, and a mismatch is what makes a runtime bump self-healing: the embedded
// tar simply stops being looked for, and the instance republishes under the new
// key. The stored object is gzipped and the embedded one is not — the zip
// container compresses it instead, and Lambda unzips the package before INIT,
// unbilled.
func embeddedTarPath(cacheKey string) (string, error) {
	base := path.Base(cacheKey)
	if !strings.HasPrefix(base, "node") || !strings.HasSuffix(base, ".tar.gz") {
		return "", fmt.Errorf("cannot mirror the cache key %q: its name is not node<version>-<arch>.tar.gz", cacheKey)
	}
	return ".ocel/bytecode/" + strings.TrimSuffix(base, ".gz"), nil
}

// embedKeyDigestLen is how much of the cache's digest the repackaged key
// carries. It only has to separate the caches one function's single artifact
// hash could pair with, so this is far past collision-proof and stays legible
// beside the hash it extends.
const embedKeyDigestLen = 13

// embeddedArtifactKey is where a repackaged bundle lands: the original
// content-addressed key extended with the digest of the cache embedded in it,
// so the key still changes iff the bytes do.
//
// Writing a new object rather than replacing the original is what keeps this
// safe against Pulumi: every deployment gets a fresh app-deploy stack (the
// plan embeds the DeploymentIdentity) which is never updated, so the code this
// pass moves a function onto cannot drift against state a later deploy reads.
// Overwriting the original key would corrupt the dedup every other deploy of
// the same source relies on.
func embeddedArtifactKey(originalKey, cacheDigest string) (string, error) {
	if !strings.HasSuffix(originalKey, ".zip") {
		return "", fmt.Errorf("cannot extend the artifact key %q: it is not a .zip", originalKey)
	}
	digest := cacheDigest
	if len(digest) > embedKeyDigestLen {
		digest = digest[:embedKeyDigestLen]
	}
	return fmt.Sprintf("%s-bc-%s.zip", strings.TrimSuffix(originalKey, ".zip"), digest), nil
}

// mergeEmbeddedTar writes the zip at srcZip out to dst with one entry added:
// the file at tarPath, at name.
//
// Existing entries go through zip.Writer.Copy — their already-deflated bytes
// verbatim, never re-compressed. That is both the cheap path (deflating a Next
// tree again would cost seconds per function) and the correct one: the function
// code lands byte-identical to what the warm pass just exercised, so nothing
// about this pass can change what the bundle does.
//
// Everything streams through dst on disk. An in-memory merge would hold the
// original package and its copy at once, per concurrent function — gigabytes
// across a multi-app deploy, for a pass that is only an optimization.
func mergeEmbeddedTar(dst, srcZip, tarPath, name string) error {
	src, err := zip.OpenReader(srcZip)
	if err != nil {
		return fmt.Errorf("read package %s: %w", srcZip, err)
	}
	defer src.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer out.Close()

	zw := zip.NewWriter(out)
	for _, f := range src.File {
		if f.Name == name {
			return fmt.Errorf("package %s already holds %s", srcZip, name)
		}
		if err := zw.Copy(f); err != nil {
			return fmt.Errorf("copy %s: %w", f.Name, err)
		}
	}
	w, err := zw.Create(name)
	if err != nil {
		return err
	}
	if err := copyFileInto(w, tarPath); err != nil {
		return fmt.Errorf("embed %s: %w", name, err)
	}
	if err := zw.Close(); err != nil {
		return err
	}
	return out.Close()
}

// fetchCacheTar downloads a published cache and unzips the gzip layer off it,
// leaving a plain tar at dst. It returns that tar's size — the number the gate
// weighs — and the digest of its bytes, which keys the repackaged artifact.
func (p embedPass) fetchCacheTar(ctx context.Context, target embedTarget, dst string) (int64, string, error) {
	out, err := p.objects.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(target.CacheBucket),
		Key:    aws.String(target.CacheKey),
	})
	if err != nil {
		return 0, "", fmt.Errorf("get %s/%s: %w", target.CacheBucket, target.CacheKey, err)
	}
	defer out.Body.Close()

	gz, err := gzip.NewReader(out.Body)
	if err != nil {
		return 0, "", err
	}
	f, err := os.Create(dst)
	if err != nil {
		return 0, "", err
	}
	defer f.Close()

	// Bounded on the way out rather than trusted: this decompresses a remote
	// object onto the deploy host's disk, and anything past the hard ceiling is
	// something the gate is about to refuse anyway.
	digest := sha256.New()
	n, err := io.Copy(io.MultiWriter(f, digest), io.LimitReader(gz, embedUnzippedCeiling+1))
	if err != nil {
		return 0, "", err
	}
	if err := f.Close(); err != nil {
		return 0, "", err
	}
	return n, hex.EncodeToString(digest.Sum(nil)), nil
}

// fetchObject streams bucket/key to dst, refusing anything past limit.
func (p embedPass) fetchObject(ctx context.Context, bucket, key, dst string, limit int64) error {
	out, err := p.objects.GetObject(ctx, &s3.GetObjectInput{
		Bucket: aws.String(bucket),
		Key:    aws.String(key),
	})
	if err != nil {
		return fmt.Errorf("get %s/%s: %w", bucket, key, err)
	}
	defer out.Body.Close()

	f, err := os.Create(dst)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(f, io.LimitReader(out.Body, limit+1))
	if err != nil {
		return err
	}
	if n > limit {
		return fmt.Errorf("%s/%s is larger than %d MiB", bucket, key, limit>>20)
	}
	return f.Close()
}

// putFile uploads a local file to bucket/key. It streams from disk rather than
// through a buffer, for the same reason the merge does.
func (p embedPass) putFile(ctx context.Context, bucket, key, src string) error {
	f, err := os.Open(src)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	_, err = p.uploader.PutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		Body:          f,
		ContentLength: aws.Int64(info.Size()),
	})
	if err != nil {
		return fmt.Errorf("upload artifact %s/%s: %w", bucket, key, err)
	}
	return nil
}

// errUpdateUnsettled marks the outcomes where UpdateFunctionCode was accepted
// but this side never saw it land. It is the one failure in the whole pass that
// does not leave the function on the package it was deployed with — the
// function is already moving — so it must be reported as its own thing rather
// than folded in with the failures that changed nothing.
var errUpdateUnsettled = errors.New("the code update did not settle in time")

// updateCode moves the function onto key and waits for the update to settle.
// The wait is not optional: an invoke against a function whose LastUpdateStatus
// is still InProgress can fail, and the verify invoke is the next thing that
// happens. A size rejection arrives here, as an error from the update — the
// gate's arithmetic is this side's best estimate, and AWS's is the real one.
//
// The pass deadline is checked before the update rather than only during the
// wait. An update issued with no time left to see it through leaves a function
// mid-flight with the promote immediately after, and nothing this side does
// afterwards can put it back; declining to start one is what keeps the pass's
// worst case a bundle that was never touched.
func (p embedPass) updateCode(ctx context.Context, target embedTarget, key string) error {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < p.settle {
		return fmt.Errorf("the embed pass has under %s left, too little to settle a code update", p.settle)
	}
	if _, err := p.code.UpdateFunctionCode(ctx, &lambda.UpdateFunctionCodeInput{
		FunctionName: aws.String(target.FunctionName),
		S3Bucket:     aws.String(target.Artifact.Bucket),
		S3Key:        aws.String(key),
	}); err != nil {
		return fmt.Errorf("could not update the function's code: %w", err)
	}

	settle, cancel := context.WithTimeout(ctx, p.settle)
	defer cancel()
	for {
		out, err := p.code.GetFunctionConfiguration(settle, &lambda.GetFunctionConfigurationInput{
			FunctionName: aws.String(target.FunctionName),
		})
		if err != nil {
			return fmt.Errorf("%w: could not read it back: %v", errUpdateUnsettled, err)
		}
		switch out.LastUpdateStatus {
		case lambdatypes.LastUpdateStatusSuccessful:
			return nil
		case lambdatypes.LastUpdateStatusFailed:
			// Failed is Lambda declining the update, not losing track of it: the
			// function keeps the code it had, so this is not errUpdateUnsettled.
			return fmt.Errorf("the code update failed: %s", aws.ToString(out.LastUpdateStatusReason))
		}
		select {
		case <-settle.Done():
			return errUpdateUnsettled
		case <-time.After(embedUpdatePoll):
		}
	}
}
