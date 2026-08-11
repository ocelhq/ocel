package deploy

import (
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	lambdatypes "github.com/aws/aws-sdk-go-v2/service/lambda/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func TestBytecodeEmbedEnabled(t *testing.T) {
	t.Run("off unless asked for exactly", func(t *testing.T) {
		t.Setenv(bytecodeCacheEnv, "1")
		t.Setenv(bytecodeEmbedEnv, "")
		if bytecodeEmbedEnabled() {
			t.Error("bytecodeEmbedEnabled() = true with no override, want false")
		}
		for _, v := range []string{"true", "yes", "on", "01", "1 "} {
			t.Setenv(bytecodeEmbedEnv, v)
			if bytecodeEmbedEnabled() {
				t.Errorf("bytecodeEmbedEnabled() = true with OCEL_BYTECODE_EMBED=%q, want false (only \"1\" enables)", v)
			}
		}
		t.Setenv(bytecodeEmbedEnv, "1")
		if !bytecodeEmbedEnabled() {
			t.Error("bytecodeEmbedEnabled() = false with OCEL_BYTECODE_EMBED=1, want true")
		}
	})

	t.Run("implies caching", func(t *testing.T) {
		t.Setenv(bytecodeEmbedEnv, "1")
		t.Setenv(bytecodeCacheEnv, "")
		if bytecodeEmbedEnabled() {
			t.Error("bytecodeEmbedEnabled() = true without OCEL_BYTECODE_CACHE=1, want false")
		}
	})
}

func TestEmbedGate(t *testing.T) {
	t.Parallel()
	for _, tc := range []struct {
		name       string
		treeBytes  int64
		tarBytes   int64
		wantOK     bool
		wantReason string
	}{
		{
			name:      "admits a small cache in a small package",
			treeBytes: 40 << 20,
			tarBytes:  8 << 20,
			wantOK:    true,
		},
		{
			name:       "refuses a cache over the soft ceiling",
			treeBytes:  40 << 20,
			tarBytes:   embedCacheCeiling + 1,
			wantReason: "32 MiB",
		},
		{
			name:       "refuses a package over the unzipped limit",
			treeBytes:  embedUnzippedCeiling - (1 << 20),
			tarBytes:   2 << 20,
			wantReason: "200 MiB",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ok, why := embedGate(tc.treeBytes, tc.tarBytes)
			if ok != tc.wantOK {
				t.Fatalf("embedGate(%d, %d) = %t (%s), want %t", tc.treeBytes, tc.tarBytes, ok, why, tc.wantOK)
			}
			if tc.wantReason != "" && !strings.Contains(why, tc.wantReason) {
				t.Errorf("reason = %q, want it to name the limit it failed (%s)", why, tc.wantReason)
			}
		})
	}
}

func TestEmbeddedTarPath(t *testing.T) {
	t.Parallel()

	t.Run("mirrors the cache key basename", func(t *testing.T) {
		t.Parallel()
		path, err := embeddedTarPath("prod/proj/web/B1/bytecode/fn-abc/node24.3.1-arm64.tar.gz")
		if err != nil {
			t.Fatalf("embeddedTarPath: %v", err)
		}
		if want := ".ocel/bytecode/node24.3.1-arm64.tar"; path != want {
			t.Errorf("embeddedTarPath = %q, want %q", path, want)
		}
	})

	t.Run("rejects a key it cannot mirror", func(t *testing.T) {
		t.Parallel()
		for _, key := range []string{
			"",
			"prod/proj/web/B1/bytecode/fn-abc/node24.3.1-arm64.tar",
			"prod/proj/web/B1/bytecode/fn-abc/cache.tar.gz",
			"prod/proj/web/B1/bytecode/fn-abc/",
		} {
			if path, err := embeddedTarPath(key); err == nil {
				t.Errorf("embeddedTarPath(%q) = %q, want an error", key, path)
			}
		}
	})
}

func TestEmbeddedArtifactKey(t *testing.T) {
	t.Parallel()

	t.Run("extends the original", func(t *testing.T) {
		t.Parallel()
		key, err := embeddedArtifactKey("myproj/web-server/abc123.zip", "deadbeefdeadbeefdeadbeef")
		if err != nil {
			t.Fatalf("embeddedArtifactKey: %v", err)
		}
		if want := "myproj/web-server/abc123-bc-deadbeefdeadb.zip"; key != want {
			t.Errorf("embeddedArtifactKey = %q, want %q", key, want)
		}
	})

	t.Run("rejects a non-zip key", func(t *testing.T) {
		t.Parallel()
		if key, err := embeddedArtifactKey("myproj/web-server/abc123", "deadbeef"); err == nil {
			t.Errorf("embeddedArtifactKey = %q, want an error", key)
		}
	})
}

func TestMergeEmbeddedTar(t *testing.T) {
	t.Parallel()

	t.Run("preserves every original entry", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		original := map[string]string{
			"index.js":               "export default () => 1\n",
			"node_modules/dep/x.js":  strings.Repeat("compressible ", 500),
			".ocel/variables.enc":    "\x00\x01binary\xff",
			"nested/deep/empty.json": "",
		}
		srcZip := filepath.Join(dir, "src.zip")
		writeTestZip(t, srcZip, original)

		tarPath := filepath.Join(dir, "cache.tar")
		tarBody := []byte(strings.Repeat("tar body ", 64))
		if err := os.WriteFile(tarPath, tarBody, 0o644); err != nil {
			t.Fatal(err)
		}

		dst := filepath.Join(dir, "merged.zip")
		const entry = ".ocel/bytecode/node24.3.1-arm64.tar"
		if err := mergeEmbeddedTar(dst, srcZip, tarPath, entry); err != nil {
			t.Fatalf("mergeEmbeddedTar: %v", err)
		}

		before := readTestZip(t, srcZip)
		after := readTestZip(t, dst)
		if len(after) != len(before)+1 {
			t.Fatalf("merged zip holds %d entries, want %d", len(after), len(before)+1)
		}
		for name, got := range before {
			merged, ok := after[name]
			if !ok {
				t.Errorf("merged zip dropped %q", name)
				continue
			}
			if merged.body != got.body {
				t.Errorf("merged zip changed the contents of %q", name)
			}
			if merged.method != got.method {
				t.Errorf("%q compression method = %d, want %d (entries must be copied raw)", name, merged.method, got.method)
			}
			if merged.crc != got.crc {
				t.Errorf("%q crc32 = %d, want %d", name, merged.crc, got.crc)
			}
		}
		embedded, ok := after[entry]
		if !ok {
			t.Fatalf("merged zip has no %q", entry)
		}
		if embedded.body != string(tarBody) {
			t.Error("the embedded tar's contents did not survive the merge")
		}
		if embedded.method != zip.Deflate {
			t.Errorf("embedded tar method = %d, want Deflate: the zip container is what compresses it", embedded.method)
		}
	})

	t.Run("handles a large original", func(t *testing.T) {
		t.Parallel()
		dir := t.TempDir()
		original := map[string]string{}
		for i := range 200 {
			original[fmt.Sprintf("chunk/%03d.bin", i)] = strings.Repeat("x", 32<<10)
		}
		srcZip := filepath.Join(dir, "src.zip")
		writeTestZip(t, srcZip, original)

		tarPath := filepath.Join(dir, "cache.tar")
		if err := os.WriteFile(tarPath, []byte("tar"), 0o644); err != nil {
			t.Fatal(err)
		}
		dst := filepath.Join(dir, "merged.zip")
		if err := mergeEmbeddedTar(dst, srcZip, tarPath, ".ocel/bytecode/node24.3.1-x86_64.tar"); err != nil {
			t.Fatalf("mergeEmbeddedTar: %v", err)
		}
		if got := len(readTestZip(t, dst)); got != len(original)+1 {
			t.Fatalf("merged zip holds %d entries, want %d", got, len(original)+1)
		}
	})
}

type fakeObjects struct {
	objects map[string][]byte
	err     error
}

func (f *fakeObjects) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	body, ok := f.objects[aws.ToString(in.Bucket)+"/"+aws.ToString(in.Key)]
	if !ok {
		return nil, &s3types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{Body: io.NopCloser(bytes.NewReader(body))}, nil
}

type fakePutter struct {
	mu   sync.Mutex
	put  map[string][]byte
	err  error
	head error
}

func (f *fakePutter) HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if f.head != nil {
		return nil, f.head
	}
	return &s3.HeadObjectOutput{}, nil
}

func (f *fakePutter) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	body, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.put == nil {
		f.put = map[string][]byte{}
	}
	f.put[aws.ToString(in.Bucket)+"/"+aws.ToString(in.Key)] = body
	return &s3.PutObjectOutput{}, nil
}

func (f *fakePutter) uploaded() map[string][]byte {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.put
}

type fakeCodeUpdater struct {
	status lambdatypes.LastUpdateStatus
	err    error

	mu      sync.Mutex
	updated map[string]string
}

func (f *fakeCodeUpdater) UpdateFunctionCode(_ context.Context, in *lambda.UpdateFunctionCodeInput, _ ...func(*lambda.Options)) (*lambda.UpdateFunctionCodeOutput, error) {
	if f.err != nil {
		return nil, f.err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.updated == nil {
		f.updated = map[string]string{}
	}
	f.updated[aws.ToString(in.FunctionName)] = aws.ToString(in.S3Key)
	return &lambda.UpdateFunctionCodeOutput{}, nil
}

func (f *fakeCodeUpdater) GetFunctionConfiguration(context.Context, *lambda.GetFunctionConfigurationInput, ...func(*lambda.Options)) (*lambda.GetFunctionConfigurationOutput, error) {
	status := f.status
	if status == "" {
		status = lambdatypes.LastUpdateStatusSuccessful
	}
	return &lambda.GetFunctionConfigurationOutput{
		LastUpdateStatus:       status,
		LastUpdateStatusReason: aws.String("size"),
	}, nil
}

const embedTestCacheKey = "prod/p/web/B1/bytecode/fn/node24.3.1-arm64.tar.gz"

func embedTestPass(t *testing.T, putter *fakePutter, code *fakeCodeUpdater, invoker *fakeInvoker) (embedPass, func() string) {
	t.Helper()
	dir := t.TempDir()
	original := filepath.Join(dir, "artifact.zip")
	writeTestZip(t, original, map[string]string{"index.js": "export default () => 1\n"})
	zipped, err := os.ReadFile(original)
	if err != nil {
		t.Fatal(err)
	}
	var gzipped bytes.Buffer
	gw := gzip.NewWriter(&gzipped)
	if _, err := gw.Write([]byte(strings.Repeat("tar body ", 128))); err != nil {
		t.Fatal(err)
	}
	if err := gw.Close(); err != nil {
		t.Fatal(err)
	}

	log, out := collectLog()
	return embedPass{
		objects: &fakeObjects{objects: map[string][]byte{
			"assets/" + embedTestCacheKey: gzipped.Bytes(),
			"artifacts/p/web/abc123.zip":  zipped,
		}},
		uploader: putter,
		code:     code,
		invoker:  invoker,
		targets: []embedTarget{{
			App:          "web",
			LogicalName:  "web_bundle",
			FunctionName: "ocel-web-1",
			Artifact:     artifactRef{Bucket: "artifacts", Key: "p/web/abc123.zip"},
			CacheBucket:  "assets",
			CacheKey:     embedTestCacheKey,
			TreeBytes:    1 << 20,
		}},
		budget: time.Minute,
		settle: embedUpdateSettle,
		log:    log,
	}, out
}

func embeddedReply() *fakeInvoker {
	return answering(`{"state":"already-cached","source":"embedded","entries":51,"loaded":51}`)
}

func TestEmbedPass(t *testing.T) {
	t.Run("repackages and verifies", func(t *testing.T) {
		putter, code := &fakePutter{}, &fakeCodeUpdater{}
		pass, out := embedTestPass(t, putter, code, embeddedReply())
		pass.run(context.Background())

		log := out()
		for _, want := range []string{
			"embedding compile caches into 1 bundle", "web_bundle", "app=web",
			".ocel/bytecode/node24.3.1-arm64.tar", "embedded 1/1 compile caches",
		} {
			if !strings.Contains(log, want) {
				t.Errorf("embed log missing %q:\n%s", want, log)
			}
		}
		uploaded := putter.uploaded()
		if len(uploaded) != 1 {
			t.Fatalf("uploaded %d objects, want 1: %v", len(uploaded), uploaded)
		}
		for key, body := range uploaded {
			if !strings.HasPrefix(key, "artifacts/p/web/abc123-bc-") || !strings.HasSuffix(key, ".zip") {
				t.Errorf("uploaded key = %q, want the original key extended with the cache digest", key)
			}
			zr, err := zip.NewReader(bytes.NewReader(body), int64(len(body)))
			if err != nil {
				t.Fatalf("the uploaded object is not a zip: %v", err)
			}
			var names []string
			for _, f := range zr.File {
				names = append(names, f.Name)
			}
			if len(names) != 2 {
				t.Errorf("uploaded package holds %v, want the original entry plus the tar", names)
			}
		}
		for _, key := range code.updated {
			if _, ok := uploaded["artifacts/"+key]; !ok {
				t.Errorf("the function was moved onto %q, which was never uploaded", key)
			}
		}
	})

	t.Run("every failure degrades to a warning", func(t *testing.T) {
		for _, tc := range []struct {
			name    string
			putter  *fakePutter
			code    *fakeCodeUpdater
			invoker *fakeInvoker
			want    string
		}{
			{
				name:    "the upload is refused",
				putter:  &fakePutter{err: errors.New("access denied")},
				code:    &fakeCodeUpdater{},
				invoker: embeddedReply(),
				want:    "could not upload",
			},
			{
				name:    "the code update is refused for size",
				putter:  &fakePutter{},
				code:    &fakeCodeUpdater{err: errors.New("InvalidParameterValueException: Unzipped size must be smaller than 262144000 bytes")},
				invoker: embeddedReply(),
				want:    "left on its original package",
			},
			{
				name:    "the code update settles as failed",
				putter:  &fakePutter{},
				code:    &fakeCodeUpdater{status: lambdatypes.LastUpdateStatusFailed},
				invoker: embeddedReply(),
				want:    "left on its original package",
			},
			{
				name:    "the function still answers from S3",
				putter:  &fakePutter{},
				code:    &fakeCodeUpdater{},
				invoker: answering(`{"state":"already-cached","source":"s3"}`),
				want:    `still answered from "s3"`,
			},
			{
				name:   "the verify invoke fails",
				putter: &fakePutter{},
				code:   &fakeCodeUpdater{},
				invoker: &fakeInvoker{respond: func(context.Context, string) (*lambda.InvokeOutput, error) {
					return nil, errors.New("boom")
				}},
				want: "could not verify",
			},
		} {
			t.Run(tc.name, func(t *testing.T) {
				pass, out := embedTestPass(t, tc.putter, tc.code, tc.invoker)
				pass.run(context.Background())

				log := out()
				if !strings.Contains(log, tc.want) {
					t.Errorf("embed log missing %q:\n%s", tc.want, log)
				}
				if !strings.Contains(log, "embedded 0/1 compile caches") {
					t.Errorf("embed log counted a success it did not have:\n%s", log)
				}
			})
		}
	})

	t.Run("missing cache is a warning", func(t *testing.T) {
		pass, out := embedTestPass(t, &fakePutter{}, &fakeCodeUpdater{}, embeddedReply())
		pass.objects = &fakeObjects{}
		pass.run(context.Background())

		if log := out(); !strings.Contains(log, "could not read the published cache") {
			t.Errorf("embed log missing the missing-cache warning:\n%s", log)
		}
	})

	t.Run("oversized package is never uploaded", func(t *testing.T) {
		putter := &fakePutter{}
		pass, out := embedTestPass(t, putter, &fakeCodeUpdater{}, embeddedReply())
		pass.targets[0].TreeBytes = embedUnzippedCeiling
		pass.run(context.Background())

		if log := out(); !strings.Contains(log, "over the 200 MiB limit") {
			t.Errorf("embed log missing the legality refusal:\n%s", log)
		}
		if len(putter.uploaded()) != 0 {
			t.Error("an oversized package was uploaded anyway")
		}
	})

	t.Run("declines an update it cannot wait out", func(t *testing.T) {
		putter, code := &fakePutter{}, &fakeCodeUpdater{}
		pass, out := embedTestPass(t, putter, code, embeddedReply())
		pass.budget = 250 * time.Millisecond
		pass.settle = time.Minute
		pass.run(context.Background())

		if len(code.updated) != 0 {
			t.Errorf("the pass issued a code update it could not wait out: %v", code.updated)
		}
		log := out()
		for _, want := range []string{"too little to settle", "left on its original package", "embedded 0/1"} {
			if !strings.Contains(log, want) {
				t.Errorf("embed log missing %q:\n%s", want, log)
			}
		}
	})

	t.Run("unsettled update is never reported as untouched", func(t *testing.T) {
		putter := &fakePutter{}
		code := &fakeCodeUpdater{status: lambdatypes.LastUpdateStatusInProgress}
		invoker := embeddedReply()
		pass, out := embedTestPass(t, putter, code, invoker)
		pass.settle = 10 * time.Millisecond
		pass.run(context.Background())

		log := out()
		if strings.Contains(log, "left on its original package") {
			t.Errorf("the pass reported an in-flight update as untouched:\n%s", log)
		}
		for _, want := range []string{"did not settle in time", "moving onto", "embedded 0/1"} {
			if !strings.Contains(log, want) {
				t.Errorf("embed log missing %q:\n%s", want, log)
			}
		}
		if calls := invoker.called(); len(calls) != 0 {
			t.Errorf("the pass verified a function mid-update: %v", calls)
		}
	})
}

func TestEmbedBytecodeCaches(t *testing.T) {
	t.Run("names the clients it is missing", func(t *testing.T) {
		t.Setenv(bytecodeCacheEnv, "1")
		t.Setenv(bytecodeEmbedEnv, "1")
		log, out := collectLog()

		embedBytecodeCaches(context.Background(), Config{}, nextManifest(), nil, nil, log)

		for _, want := range []string{"object getter", "code updater", "artifact uploader", "function invoker", "not embedding"} {
			if !strings.Contains(out(), want) {
				t.Errorf("embed log missing %q:\n%s", want, out())
			}
		}
	})
}

func TestEmbedTargets(t *testing.T) {
	t.Run("keeps a bundle that already answered from its package", func(t *testing.T) {
		root := t.TempDir()
		if err := os.MkdirAll(filepath.Join(root, "web.func"), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(root, "web.func", "index.js"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		manifest := &deploymentsv1.Manifest{
			Slug:      "proj",
			Functions: []*deploymentsv1.ManifestFunction{{LogicalName: "web_index", Framework: "next", App: "web", ArtifactPath: "web.func"}},
		}
		warmed := []warmResult{{
			Target: warmTarget{App: "web", LogicalName: "web_index", FunctionName: "ocel-web-index"},
			Reply:  warmReply{Key: embedTestCacheKey, Source: warmSourceEmbedded},
		}}

		log, out := collectLog()
		targets := embedTargets(
			Config{ArtifactRoot: root},
			manifest,
			map[string]*isrConfig{"web": {Bucket: "assets", Prefix: "prod/proj/web/B1"}},
			map[string]artifactRef{"web_index": {Bucket: "artifacts", Key: "proj/web/abc123.zip"}},
			warmed,
			log,
		)

		if len(targets) != 1 {
			t.Fatalf("embedTargets = %+v, want the bundle kept rather than silently dropped (log: %s)", targets, out())
		}
		if targets[0].CacheKey != embedTestCacheKey {
			t.Errorf("embedTargets[0].CacheKey = %q, want the key the membrane reported", targets[0].CacheKey)
		}
	})
}

type testZipEntry struct {
	body   string
	method uint16
	crc    uint32
}

func writeTestZip(t *testing.T, path string, entries map[string]string) {
	t.Helper()
	var buf bytes.Buffer
	zw := zip.NewWriter(&buf)
	for name, body := range entries {
		w, err := zw.Create(name)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := io.WriteString(w, body); err != nil {
			t.Fatal(err)
		}
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, buf.Bytes(), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readTestZip(t *testing.T, path string) map[string]testZipEntry {
	t.Helper()
	zr, err := zip.OpenReader(path)
	if err != nil {
		t.Fatalf("open %s: %v", path, err)
	}
	defer zr.Close()
	entries := map[string]testZipEntry{}
	for _, f := range zr.File {
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s in %s: %v", f.Name, path, err)
		}
		body, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s in %s: %v", f.Name, path, err)
		}
		entries[f.Name] = testZipEntry{body: string(body), method: f.Method, crc: f.CRC32}
	}
	return entries
}
