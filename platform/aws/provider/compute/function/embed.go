package function

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

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
)

const embedCacheCeiling = 32 << 20

const embedUnzippedCeiling = 200 << 20

const embedConcurrency = 4

const embedBundleDeadline = 3 * time.Minute

var embedSlots = make(chan struct{}, embedConcurrency)

const embedUpdatePoll = 500 * time.Millisecond

const embedUpdateWait = 45 * time.Second

type ObjectsAPI interface {
	GetObject(ctx context.Context, in *s3.GetObjectInput, optFns ...func(*s3.Options)) (*s3.GetObjectOutput, error)
}

type FunctionCodeAPI interface {
	UpdateFunctionCode(ctx context.Context, in *lambda.UpdateFunctionCodeInput, optFns ...func(*lambda.Options)) (*lambda.UpdateFunctionCodeOutput, error)
	GetFunctionConfiguration(ctx context.Context, in *lambda.GetFunctionConfigurationInput, optFns ...func(*lambda.Options)) (*lambda.GetFunctionConfigurationOutput, error)
}

type embedTarget struct {
	App          string
	LogicalName  string
	FunctionName string

	Artifact        Artifact
	ArtifactsBucket string
	BytecodeKey     string
	TreeBytes       int64
}

func missingEmbedClients(clients *Clients) string {
	var missing []string
	for _, c := range []struct {
		name    string
		present bool
	}{
		{"object getter", clients.Getter != nil},
		{"code updater", clients.CodeUpdater != nil},
		{"artifact object store", clients.Buckets.Objects != nil},
		{"function invoker", clients.Invoker != nil},
	} {
		if !c.present {
			missing = append(missing, c.name)
		}
	}
	return strings.Join(missing, ", ")
}

type embedPass struct {
	objects        ObjectsAPI
	store          payloads.ObjectStore
	code           FunctionCodeAPI
	invoke         InvokeAPI
	targets        []embedTarget
	bundleDeadline time.Duration
	updateWait     time.Duration
	progress       progress.Log
}

func (p embedPass) run(ctx context.Context) {
	if len(p.targets) == 0 || p.objects == nil || p.code == nil {
		return
	}
	p.progress.Say(fmt.Sprintf("Embedding compile caches into %s, %d at a time", plural(len(p.targets), "bundle", "bundles"), embedConcurrency))

	start := time.Now()
	var (
		mu       sync.Mutex
		embedded int
		skipped  = make([]bool, len(p.targets))
	)
	var group sync.WaitGroup
	for i, target := range p.targets {
		group.Go(func() {
			release, err := takeSlot(ctx, embedSlots)
			if err != nil {
				skipped[i] = true
				return
			}
			defer release()
			embedding, cancel := context.WithTimeout(ctx, p.bundleDeadline)
			defer cancel()
			at := time.Now()
			outcome, ok := p.embedOne(embedding, target)
			mu.Lock()
			defer mu.Unlock()
			if ok {
				embedded++
			}
			bundleOutcome(p.progress, ok)(fmt.Sprintf("%s of app %s: %s in %.1fs", target.LogicalName, target.App, outcome, time.Since(at).Seconds()))
		})
	}
	group.Wait()

	for i, target := range p.targets {
		if skipped[i] {
			p.progress.Warn(fmt.Sprintf("%s of app %s: not embedded, the deploy stopped first", target.LogicalName, target.App))
		}
	}
	passOutcome(p.progress, embedded, len(p.targets))(fmt.Sprintf("Embedded %d/%d compile caches in %.0fs", embedded, len(p.targets), time.Since(start).Seconds()))
}

func (p embedPass) embedOne(ctx context.Context, target embedTarget) (string, bool) {
	entry, err := embeddedTarPath(target.BytecodeKey)
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
		if errors.Is(err, errUpdateUnfinished) {
			return fmt.Sprintf("embedded %s, but %v; the function is moving onto %s unverified", entry, err, key), false
		}
		return fmt.Sprintf("%v; left on its original package", err), false
	}

	reply, failure := invokeWarm(ctx, p.invoke, target.FunctionName)
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
	return buildoutput.StateDir + "/bytecode/" + strings.TrimSuffix(base, ".gz"), nil
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
			return fmt.Errorf("package %s already contains %s", srcZip, name)
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
		Bucket: aws.String(target.ArtifactsBucket),
		Key:    aws.String(target.BytecodeKey),
	})
	if err != nil {
		return 0, "", fmt.Errorf("get %s/%s: %w", target.ArtifactsBucket, target.BytecodeKey, err)
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
	_, err = p.store.PutObject(ctx, &s3.PutObjectInput{
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

var errUpdateUnfinished = errors.New("the code update did not finish in time")

func (p embedPass) updateCode(ctx context.Context, target embedTarget, key string) error {
	if deadline, ok := ctx.Deadline(); ok && time.Until(deadline) < p.updateWait {
		return fmt.Errorf("the bundle has under %s left of its embed deadline, too little to finish a code update", p.updateWait)
	}
	if _, err := p.code.UpdateFunctionCode(ctx, &lambda.UpdateFunctionCodeInput{
		FunctionName: aws.String(target.FunctionName),
		S3Bucket:     aws.String(target.Artifact.Bucket),
		S3Key:        aws.String(key),
	}); err != nil {
		return fmt.Errorf("could not update the function's code: %w", err)
	}

	polling, cancel := context.WithTimeout(ctx, p.updateWait)
	defer cancel()
	for {
		out, err := p.code.GetFunctionConfiguration(polling, &lambda.GetFunctionConfigurationInput{
			FunctionName: aws.String(target.FunctionName),
		})
		if err != nil {
			return fmt.Errorf("%w: could not read it back: %w", errUpdateUnfinished, err)
		}
		switch out.LastUpdateStatus {
		case lambdatypes.LastUpdateStatusSuccessful:
			return nil
		case lambdatypes.LastUpdateStatusFailed:
			return fmt.Errorf("the code update failed: %s", aws.ToString(out.LastUpdateStatusReason))
		}
		select {
		case <-polling.Done():
			return errUpdateUnfinished
		case <-time.After(embedUpdatePoll):
		}
	}
}

func copyFileInto(w io.Writer, path string) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	_, err = io.Copy(w, f)
	f.Close()
	return err
}
