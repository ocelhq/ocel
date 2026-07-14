package deploy

import (
	"archive/zip"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
)

// readZip decodes an in-memory zip into a relative-path -> contents map.
func readZip(t *testing.T, data []byte) map[string]string {
	t.Helper()
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}
	out := map[string]string{}
	for _, f := range zr.File {
		if f.FileInfo().IsDir() {
			continue
		}
		rc, err := f.Open()
		if err != nil {
			t.Fatalf("open %s: %v", f.Name, err)
		}
		b, err := io.ReadAll(rc)
		rc.Close()
		if err != nil {
			t.Fatalf("read %s: %v", f.Name, err)
		}
		out[f.Name] = string(b)
	}
	return out
}

// writeTree materializes a set of relative-path -> contents files under a fresh
// temp dir and returns it.
func writeTree(t *testing.T, files map[string]string) string {
	t.Helper()
	dir := t.TempDir()
	for rel, contents := range files {
		full := filepath.Join(dir, rel)
		if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(full, []byte(contents), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return dir
}

// TestHashArtifact_Deterministic proves the hash is stable across two identical
// source trees materialized independently, so content-addressing dedups: the
// same code always maps to the same key.
func TestHashArtifact_Deterministic(t *testing.T) {
	files := map[string]string{
		"src/server.js": "export const handler = () => 'hi'",
		"package.json":  `{"name":"app"}`,
	}
	a := writeTree(t, files)
	b := writeTree(t, files)

	ha, err := hashArtifact(a)
	if err != nil {
		t.Fatalf("hashArtifact(a): %v", err)
	}
	hb, err := hashArtifact(b)
	if err != nil {
		t.Fatalf("hashArtifact(b): %v", err)
	}
	if ha != hb {
		t.Errorf("hash of identical trees differ: %q vs %q", ha, hb)
	}
}

// TestHashArtifact_SensitiveToContentAndPaths proves the hash changes when a
// file's contents change and when a file is renamed, so a real code change
// yields a new key (and Pulumi redeploys the function).
func TestHashArtifact_SensitiveToContentAndPaths(t *testing.T) {
	base, err := hashArtifact(writeTree(t, map[string]string{"a.js": "one"}))
	if err != nil {
		t.Fatal(err)
	}
	changedContent, err := hashArtifact(writeTree(t, map[string]string{"a.js": "two"}))
	if err != nil {
		t.Fatal(err)
	}
	changedPath, err := hashArtifact(writeTree(t, map[string]string{"b.js": "one"}))
	if err != nil {
		t.Fatal(err)
	}
	if base == changedContent {
		t.Error("hash unchanged after a file's contents changed")
	}
	if base == changedPath {
		t.Error("hash unchanged after a file was renamed")
	}
}

// TestArtifactKey pins the content-addressed key layout: artifacts are
// structured by project then function, keyed by the source hash.
func TestArtifactKey(t *testing.T) {
	got := artifactKey("proj-123", "app_web", "abc123")
	want := "proj-123/app_web/abc123.zip"
	if got != want {
		t.Errorf("artifactKey = %q, want %q", got, want)
	}
}

// fakeUploader records PutObject calls and reports a configurable HeadObject
// existence result.
type fakeUploader struct {
	exists    map[string]bool
	puts      []string
	putBodies map[string]string
	headErr   error
}

func (f *fakeUploader) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	if f.headErr != nil {
		return nil, f.headErr
	}
	if f.exists[aws.ToString(in.Key)] {
		return &s3.HeadObjectOutput{}, nil
	}
	return nil, &s3types.NotFound{}
}

func (f *fakeUploader) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	key := aws.ToString(in.Key)
	f.puts = append(f.puts, key)
	if in.Body != nil {
		b, _ := io.ReadAll(in.Body)
		if f.putBodies == nil {
			f.putBodies = map[string]string{}
		}
		f.putBodies[key] = string(b)
	}
	return &s3.PutObjectOutput{}, nil
}

// bodyFn returns a body closure that records whether it was invoked, so a test
// can assert the zip is deferred behind the presence check.
func bodyFn(called *bool) func() ([]byte, error) {
	return func() ([]byte, error) {
		*called = true
		return []byte("data"), nil
	}
}

// TestUploadArtifact_SkipsWhenPresent proves an already-present object is not
// re-uploaded, and — crucially — the body (the expensive zip) is never invoked:
// identical redeploys are a cheap HeadObject.
func TestUploadArtifact_SkipsWhenPresent(t *testing.T) {
	f := &fakeUploader{exists: map[string]bool{"k.zip": true}}
	var zipped bool
	if err := uploadArtifact(context.Background(), f, "bucket", "k.zip", bodyFn(&zipped)); err != nil {
		t.Fatalf("uploadArtifact: %v", err)
	}
	if len(f.puts) != 0 {
		t.Errorf("PutObject called %d times, want 0 (object already present)", len(f.puts))
	}
	if zipped {
		t.Error("body (zip) was invoked despite the object already being present")
	}
}

// TestUploadArtifact_UploadsWhenMissing proves a missing object (e.g. one the
// lifecycle rule reaped) is re-uploaded, so a live function's artifact always
// exists at deploy time.
func TestUploadArtifact_UploadsWhenMissing(t *testing.T) {
	f := &fakeUploader{exists: map[string]bool{}}
	var zipped bool
	if err := uploadArtifact(context.Background(), f, "bucket", "k.zip", bodyFn(&zipped)); err != nil {
		t.Fatalf("uploadArtifact: %v", err)
	}
	if len(f.puts) != 1 || f.puts[0] != "k.zip" {
		t.Errorf("PutObject calls = %v, want single [k.zip]", f.puts)
	}
	if !zipped {
		t.Error("body (zip) was not invoked on a cache miss")
	}
}

// TestUploadArtifact_HeadErrorSurfaces proves a non-NotFound HeadObject error
// aborts rather than being mistaken for "missing" (which could mask an outage).
func TestUploadArtifact_HeadErrorSurfaces(t *testing.T) {
	f := &fakeUploader{headErr: errors.New("access denied")}
	var zipped bool
	if err := uploadArtifact(context.Background(), f, "bucket", "k.zip", bodyFn(&zipped)); err == nil {
		t.Fatal("uploadArtifact = nil, want the HeadObject error surfaced")
	}
	if len(f.puts) != 0 {
		t.Errorf("PutObject called despite HeadObject error: %v", f.puts)
	}
}

// TestZipDir_RoundTrips proves the produced archive contains every source file
// at its relative path with its contents, so the Lambda package matches the
// .func tree.
func TestZipDir_PreservesSymlinks(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "real.js"), []byte("module.exports={}"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("real.js", filepath.Join(dir, "link.js")); err != nil {
		t.Fatal(err)
	}

	data, err := zipDir(dir)
	if err != nil {
		t.Fatalf("zipDir: %v", err)
	}
	zr, err := zip.NewReader(bytes.NewReader(data), int64(len(data)))
	if err != nil {
		t.Fatalf("open zip: %v", err)
	}

	entries := map[string]*zip.File{}
	for _, f := range zr.File {
		entries[f.Name] = f
	}
	link, ok := entries["link.js"]
	if !ok {
		t.Fatal("symlink entry missing from zip")
	}
	if link.Mode()&os.ModeSymlink == 0 {
		t.Errorf("link.js zipped as mode %v, want a symlink", link.Mode())
	}
	rc, err := link.Open()
	if err != nil {
		t.Fatal(err)
	}
	target, _ := io.ReadAll(rc)
	rc.Close()
	if string(target) != "real.js" {
		t.Errorf("symlink target = %q, want %q", target, "real.js")
	}
	if entries["real.js"].Mode()&os.ModeSymlink != 0 {
		t.Error("real.js should be a regular file, not a symlink")
	}
}

func TestHashArtifact_SensitiveToSymlinkTarget(t *testing.T) {
	build := func(target string) string {
		dir := t.TempDir()
		if err := os.WriteFile(filepath.Join(dir, "a.js"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, "b.js"), []byte("x"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink(target, filepath.Join(dir, "link.js")); err != nil {
			t.Fatal(err)
		}
		h, err := hashArtifact(dir)
		if err != nil {
			t.Fatalf("hashArtifact: %v", err)
		}
		return h
	}
	if build("a.js") == build("b.js") {
		t.Error("hash ignored the symlink target")
	}
}

func TestZipDir_RoundTrips(t *testing.T) {
	dir := writeTree(t, map[string]string{
		"src/server.js": "handler",
		"package.json":  "{}",
	})
	data, err := zipDir(dir)
	if err != nil {
		t.Fatalf("zipDir: %v", err)
	}
	got := readZip(t, data)
	want := map[string]string{"src/server.js": "handler", "package.json": "{}"}
	if len(got) != len(want) {
		t.Fatalf("zip entries = %v, want %v", got, want)
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("zip[%q] = %q, want %q", k, got[k], v)
		}
	}
}
