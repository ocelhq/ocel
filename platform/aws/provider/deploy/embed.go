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

const embedCacheCeiling = 32 << 20

const embedUnzippedCeiling = 200 << 20

const embedConcurrency = appConcurrency

const embedPassDeadline = 3 * time.Minute

const embedUpdatePoll = 500 * time.Millisecond

const embedUpdateSettle = 45 * time.Second

type ObjectGetter interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

type FunctionCodeUpdater interface {
	UpdateFunctionCode(ctx context.Context, in *lambda.UpdateFunctionCodeInput, optFns ...func(*lambda.Options)) (*lambda.UpdateFunctionCodeOutput, error)
	GetFunctionConfiguration(ctx context.Context, in *lambda.GetFunctionConfigurationInput, optFns ...func(*lambda.Options)) (*lambda.GetFunctionConfigurationOutput, error)
}

type embedTarget struct {
	App          string
	LogicalName  string
	FunctionName string

	Artifact    artifactRef
	CacheBucket string
	CacheKey    string
	TreeBytes   int64
}

func embedBytecodeCaches(ctx context.Context, cfg Config, manifest *deploymentsv1.Manifest, artifacts map[string]artifactRef, warmed []warmResult, builds appBuilds, log func(string)) {
	if log == nil {
		log = func(string) {}
	}
	if !bytecodeEmbedRequested() {
		return
	}
	if !bytecodeEmbedEnabled() {
		log(fmt.Sprintf("ocel: %s=1 has nothing to embed without %s=1; not embedding", bytecodeEmbedEnv, bytecodeCacheEnv))
		return
	}
	if missing := missingEmbedClients(cfg); missing != "" {
		log(fmt.Sprintf("ocel: %s=1 but this deploy has no %s; not embedding", bytecodeEmbedEnv, missing))
		return
	}
	embedPass{
		objects:  cfg.Getter,
		uploader: cfg.Uploader,
		code:     cfg.CodeUpdater,
		invoker:  cfg.Invoker,
		targets:  embedTargets(cfg, manifest, builds.bytecode, artifacts, warmed, log),
		budget:   embedPassDeadline,
		settle:   embedUpdateSettle,
		log:      log,
	}.run(ctx)
}

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

func embedTargets(cfg Config, manifest *deploymentsv1.Manifest, bytecode map[string]*bytecodeConfig, artifacts map[string]artifactRef, warmed []warmResult, log func(string)) []embedTarget {
	dirs := map[string]string{}
	for _, fn := range manifest.GetFunctions() {
		dirs[fn.GetLogicalName()] = artifactArchivePath(cfg.ArtifactRoot, fn.GetArtifactPath())
	}
	var targets []embedTarget
	for _, result := range warmed {
		logical := result.Target.LogicalName
		cache := bytecode[result.Target.App]
		artifact := artifacts[logical]
		if cache == nil || artifact.Key == "" || result.Reply.Key == "" {
			continue
		}
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
	_ = g.Wait()

	for i, target := range p.targets {
		if skipped[i] {
			p.log(fmt.Sprintf("  %s app=%s  the embed pass ran out of time; not embedded", target.LogicalName, target.App))
		}
	}
	p.log(fmt.Sprintf("ocel: embedded %d/%d compile caches in %.0fs", embedded, len(p.targets), time.Since(start).Seconds()))
}

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
			return fmt.Sprintf("embedded %s, but %v; the function is moving onto %s unverified", entry, err, key), false
		}
		return fmt.Sprintf("%v; left on its original package", err), false
	}

	reply, failure := invokeWarm(ctx, p.invoker, target.FunctionName)
	switch {
	case failure != "":
		return fmt.Sprintf("embedded %s, but could not verify it: %s", entry, failure), false
	case reply.Source != warmSourceEmbedded:
		return fmt.Sprintf("embedded %s, but the function still answered from %q; it will keep fetching from S3", entry, reply.Source), false
	}
	return fmt.Sprintf("embedded %s (%.1f MiB) at %s", entry, float64(tarBytes)/(1<<20), key), true
}

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

func embeddedTarPath(cacheKey string) (string, error) {
	base := path.Base(cacheKey)
	if !strings.HasPrefix(base, "node") || !strings.HasSuffix(base, ".tar.gz") {
		return "", fmt.Errorf("cannot mirror the cache key %q: its name is not node<version>-<arch>.tar.gz", cacheKey)
	}
	return ".ocel/bytecode/" + strings.TrimSuffix(base, ".gz"), nil
}

const embedKeyDigestLen = 13

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

var errUpdateUnsettled = errors.New("the code update did not settle in time")

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
			return fmt.Errorf("the code update failed: %s", aws.ToString(out.LastUpdateStatusReason))
		}
		select {
		case <-settle.Done():
			return errUpdateUnsettled
		case <-time.After(embedUpdatePoll):
		}
	}
}
