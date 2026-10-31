package agent_test

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"

	"github.com/ocelhq/ocel/pkg/runner/agent"
)

type blobs map[string][]byte

func (b blobs) file(path string, body string, executable bool) *runnerv1.SourceFile {
	sum := sha256.Sum256([]byte(body))
	digest := hex.EncodeToString(sum[:])
	b[digest] = []byte(body)
	return &runnerv1.SourceFile{Path: path, Digest: digest, Size: uint64(len(body)), Executable: executable}
}

func (b blobs) open(_ context.Context, digest string) (io.ReadCloser, error) {
	body, ok := b[digest]
	if !ok {
		return nil, fmt.Errorf("no blob %s", digest)
	}
	return io.NopCloser(bytes.NewReader(body)), nil
}

func link(path, target string) *runnerv1.SourceFile {
	return &runnerv1.SourceFile{Path: path, SymlinkTarget: target}
}

func TestASourceIsWrittenAsTheCLIReadIt(t *testing.T) {
	b := blobs{}
	dir := filepath.Join(t.TempDir(), "source")
	source := &runnerv1.SourceManifest{Files: []*runnerv1.SourceFile{
		b.file("package.json", "{}\n", false),
		b.file("bin/run", "#!/bin/sh\necho hi\n", true),
		link("current", "bin/run"),
		link("web/shared", "../lib"),
		b.file("lib/a.js", "export {};\n", false),
	}}
	if err := agent.Materialise(context.Background(), dir, source, b.open); err != nil {
		t.Fatal(err)
	}
	if body, _ := os.ReadFile(filepath.Join(dir, "current")); string(body) != "#!/bin/sh\necho hi\n" {
		t.Fatalf("current reads %q through its symlink", body)
	}
	if info, _ := os.Stat(filepath.Join(dir, "bin/run")); info.Mode().Perm()&0o111 == 0 {
		t.Fatalf("bin/run has mode %v, want it executable", info.Mode())
	}
	if info, _ := os.Stat(filepath.Join(dir, "package.json")); info.Mode().Perm()&0o111 != 0 {
		t.Fatalf("package.json has mode %v, want it not executable", info.Mode())
	}
	if target, _ := os.Readlink(filepath.Join(dir, "web/shared")); target != "../lib" {
		t.Fatalf("web/shared points at %q", target)
	}
	if _, err := os.Stat(filepath.Join(dir, "web/shared/a.js")); err != nil {
		t.Fatalf("web/shared/a.js through its symlink: %v", err)
	}
}

func TestASourceThatLeavesItsDirectoryIsRefused(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files func(b blobs) []*runnerv1.SourceFile
		want  string
	}{
		{"a path climbing out", func(b blobs) []*runnerv1.SourceFile { return []*runnerv1.SourceFile{b.file("../escape", "x", false)} }, "leaves the source"},
		{"an absolute path", func(b blobs) []*runnerv1.SourceFile { return []*runnerv1.SourceFile{b.file("/etc/escape", "x", false)} }, "not relative"},
		{"a symlink climbing out", func(blobs) []*runnerv1.SourceFile { return []*runnerv1.SourceFile{link("a/up", "../../outside")} }, "leaves the source"},
		{"an absolute symlink", func(blobs) []*runnerv1.SourceFile { return []*runnerv1.SourceFile{link("etc", "/etc")} }, "leaves the source"},
		{"a climb after a symlink to the root", func(blobs) []*runnerv1.SourceFile {
			return []*runnerv1.SourceFile{link("here", "."), link("sneaky", "here/..")}
		}, "leaves the source"},
		{"symlinks in a loop", func(blobs) []*runnerv1.SourceFile { return []*runnerv1.SourceFile{link("a", "b"), link("b", "a")} }, "symlinks"},
		{"a file under a symlink", func(b blobs) []*runnerv1.SourceFile {
			return []*runnerv1.SourceFile{link("lib", "vendor"), b.file("lib/x.js", "x", false)}
		}, "under lib, which is a symlink"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			parent := t.TempDir()
			dir := filepath.Join(parent, "source")
			err := agent.Materialise(context.Background(), dir, &runnerv1.SourceManifest{Files: tt.files(blobs{})}, blobs{}.open)
			if err == nil || !strings.Contains(err.Error(), tt.want) {
				t.Fatalf("err = %v, want it to say %q", err, tt.want)
			}
			entries, _ := os.ReadDir(parent)
			for _, entry := range entries {
				if entry.Name() != "source" {
					t.Fatalf("%s was written beside the source", entry.Name())
				}
			}
		})
	}
}

func TestAFileWhoseBlobIsNotItsBytesIsRefused(t *testing.T) {
	b := blobs{}
	file := b.file("a.txt", "meant", false)
	b[file.GetDigest()] = []byte("other")
	err := agent.Materialise(context.Background(), t.TempDir(), &runnerv1.SourceManifest{Files: []*runnerv1.SourceFile{file}}, b.open)
	if err == nil || !strings.Contains(err.Error(), "hashes to") {
		t.Fatalf("err = %v, want the digest mismatch named", err)
	}
	file.Size = 3
	err = agent.Materialise(context.Background(), t.TempDir(), &runnerv1.SourceManifest{Files: []*runnerv1.SourceFile{file}}, b.open)
	if err == nil || !strings.Contains(err.Error(), "bytes") {
		t.Fatalf("err = %v, want the size mismatch named", err)
	}
}

func fanningLinks() []*runnerv1.SourceFile {
	files := []*runnerv1.SourceFile{link("l0", ".")}
	for i := 1; i < 10; i++ {
		previous := fmt.Sprintf("l%d", i-1)
		files = append(files, link(fmt.Sprintf("l%d", i), strings.Repeat(previous+"/", 12)+"."))
	}
	return files
}

func TestSymlinksThatFanOutAreRefusedPromptly(t *testing.T) {
	done := make(chan error, 1)
	go func() {
		done <- agent.Materialise(context.Background(), filepath.Join(t.TempDir(), "source"), &runnerv1.SourceManifest{Files: fanningLinks()}, blobs{}.open)
	}()
	select {
	case err := <-done:
		if err == nil || !strings.Contains(err.Error(), "more than 40 symlinks") {
			t.Fatalf("err = %v, want the symlinks refused for passing through more than 40", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("validating ten symlinks took more than 5s")
	}
}

func TestAnEndedContextStopsASourceBeforeItsSymlinksAreChecked(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err := agent.Materialise(ctx, filepath.Join(t.TempDir(), "source"), &runnerv1.SourceManifest{Files: []*runnerv1.SourceFile{link("a", "b"), link("b", ".")}}, blobs{}.open)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err = %v, want the context's", err)
	}
}

func TestASourceOfMoreThan2GiBIsRefusedBeforeAnyBlobIsRead(t *testing.T) {
	for _, tt := range []struct {
		name  string
		sizes []uint64
	}{
		{"past the cap", []uint64{1 << 30, 1 << 30, 1}},
		{"wrapping the sum", []uint64{math.MaxUint64, 2}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			var files []*runnerv1.SourceFile
			for i, size := range tt.sizes {
				files = append(files, &runnerv1.SourceFile{Path: fmt.Sprintf("f%d", i), Digest: strings.Repeat("0", 64), Size: size})
			}
			opened := false
			open := func(context.Context, string) (io.ReadCloser, error) {
				opened = true
				return nil, errors.New("no blob")
			}
			err := agent.Materialise(context.Background(), filepath.Join(t.TempDir(), "source"), &runnerv1.SourceManifest{Files: files}, open)
			if err == nil || !strings.Contains(err.Error(), "more than 2 GiB") {
				t.Fatalf("err = %v, want the source refused for holding more than 2 GiB", err)
			}
			if opened {
				t.Fatal("a blob was read for a source refused for its size")
			}
		})
	}
}
