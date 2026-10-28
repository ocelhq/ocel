package sourcearchive_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/sourcearchive"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
)

func write(t *testing.T, root string, files map[string]string) {
	t.Helper()
	for name, body := range files {
		file := filepath.Join(root, filepath.FromSlash(name))
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(file, []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
}

func paths(archive *sourcearchive.Archive) []string {
	var listed []string
	for _, f := range archive.Manifest.GetFiles() {
		listed = append(listed, f.GetPath())
	}
	return listed
}

func sum(body string) string {
	digest := sha256.Sum256([]byte(body))
	return hex.EncodeToString(digest[:])
}

func TestWalkUploadsWhatGitWouldTrackAndTheFilesABuildNeeds(t *testing.T) {
	for _, tt := range []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{
			name:  "every plain file",
			files: map[string]string{"package.json": "{}", "src/index.ts": "x"},
			want:  []string{"package.json", "src/index.ts"},
		},
		{
			name: "dependency and build output never goes up",
			files: map[string]string{
				"index.ts":                             "x",
				"node_modules/left-pad/a.js":           "x",
				"apps/web/node_modules/b.js":           "x",
				".git/HEAD":                            "x",
				buildoutput.StateDir + "/console.json": "{}",
				"pkg/__pycache__/m.cpython.pyc":        "x",
			},
			want: []string{"index.ts"},
		},
		{
			name: "a target directory beside a Cargo.toml is Rust's output",
			files: map[string]string{
				"Cargo.toml":                  "[package]",
				"target/debug/app":            "x",
				"crates/api/Cargo.toml":       "[package]",
				"crates/api/target/debug/api": "x",
			},
			want: []string{"Cargo.toml", "crates/api/Cargo.toml"},
		},
		{
			name:  "a target directory with no Cargo.toml beside it is source",
			files: map[string]string{"package.json": "{}", "src/target/index.ts": "x", "target/a.ts": "x"},
			want:  []string{"package.json", "src/target/index.ts", "target/a.ts"},
		},
		{
			name: "a virtualenv is dropped by its pyvenv.cfg, whatever its name",
			files: map[string]string{
				"main.py":                "x",
				".venv/pyvenv.cfg":       "home = /usr/bin",
				".venv/bin/python":       "x",
				"venv/pyvenv.cfg":        "home = /usr/bin",
				"venv/lib/x.py":          "x",
				"api/env/pyvenv.cfg":     "home = /usr/bin",
				"api/env/lib/y.py":       "x",
				"src/venv/handlers.py":   "x",
				"src/.venv/notes.txt":    "x",
				"api/pyvenv.cfg.example": "x",
			},
			want: []string{"api/pyvenv.cfg.example", "main.py", "src/.venv/notes.txt", "src/venv/handlers.py"},
		},
		{
			name:  "dotenv files never go up",
			files: map[string]string{".env": "SECRET=1", ".env.local": "x", "apps/web/.env.production": "x", "env.ts": "x"},
			want:  []string{"env.ts"},
		},
		{
			name: "credentials never go up",
			files: map[string]string{
				"index.ts":                     "x",
				"src/key.ts":                   "x",
				"id_rsa":                       "x",
				"deploy/id_ed25519.pub":        "x",
				"deploy/server.pem":            "x",
				"certs/tls.key":                "x",
				"keystore.p12":                 "x",
				".netrc":                       "x",
				".git-credentials":             "x",
				".pgpass":                      "x",
				".pypirc":                      "x",
				".aws/credentials":             "x",
				"home/.ssh/config":             "x",
				".gnupg/pubring.kbx":           "x",
				".kube/config":                 "x",
				".docker/config.json":          "{}",
				"apps/web/.docker/config.json": "{}",
				".docker/daemon.json":          "{}",
			},
			want: []string{".docker/daemon.json", "index.ts", "src/key.ts"},
		},
		{
			name:  "an ignore file can include a credential again",
			files: map[string]string{".gitignore": "!certs/ca.pem\n", "certs/ca.pem": "x", "certs/server.pem": "x"},
			want:  []string{".gitignore", "certs/ca.pem"},
		},
		{
			name: "an .npmrc holding a registry credential stays behind",
			files: map[string]string{
				".npmrc":                "registry=https://npm.example.com\n//npm.example.com/:_authToken=npm_abc\n",
				"apps/web/.npmrc":       "; scoped\n_auth = dXNlcjpwYXNz\n",
				"apps/api/.npmrc":       "//npm.example.com/:_authToken=${NPM_TOKEN}\nnode-linker=hoisted\n",
				"apps/docs/.npmrc":      "auto-install-peers=true\n# _authToken=npm_old\n",
				"apps/web/package.json": "{}",
			},
			want: []string{"apps/api/.npmrc", "apps/docs/.npmrc", "apps/web/package.json"},
		},
		{
			name:  "the root .gitignore excludes",
			files: map[string]string{".gitignore": "dist/\n*.log\n# comment\n\n", "dist/a.js": "x", "debug.log": "x", "src/trace.log": "x", "src/a.ts": "x"},
			want:  []string{".gitignore", "src/a.ts"},
		},
		{
			name:  "an anchored pattern excludes only at its own level",
			files: map[string]string{".gitignore": "/build\n", "build/a": "x", "src/build/b": "x"},
			want:  []string{".gitignore", "src/build/b"},
		},
		{
			name:  "a negation includes again",
			files: map[string]string{".gitignore": "*.json\n!package.json\n", "package.json": "{}", "data.json": "{}"},
			want:  []string{".gitignore", "package.json"},
		},
		{
			name: "a nested .gitignore applies under its own directory",
			files: map[string]string{
				"apps/web/.gitignore": "out\n",
				"apps/web/out/x.html": "x",
				"apps/api/out/y.txt":  "x",
				"apps/web/page.tsx":   "x",
			},
			want: []string{"apps/api/out/y.txt", "apps/web/.gitignore", "apps/web/page.tsx"},
		},
		{
			name:  "a nested .gitignore can include what a parent excludes",
			files: map[string]string{".gitignore": "*.txt\n", "docs/.gitignore": "!keep.txt\n", "docs/keep.txt": "x", "docs/drop.txt": "x"},
			want:  []string{".gitignore", "docs/.gitignore", "docs/keep.txt"},
		},
		{
			name:  "the .git/info/exclude excludes",
			files: map[string]string{".git/info/exclude": "scratch/\n", "scratch/a": "x", "main.go": "x"},
			want:  []string{"main.go"},
		},
		{
			name:  "the root .dockerignore excludes",
			files: map[string]string{".dockerignore": "coverage\n**/*.md\n", "coverage/lcov.info": "x", "README.md": "x", "docs/guide.md": "x", "main.go": "x"},
			want:  []string{".dockerignore", "main.go"},
		},
		{
			name:  "a .dockerignore exclusion includes again inside an excluded directory",
			files: map[string]string{".dockerignore": "assets\n!assets/logo.svg\n", "assets/logo.svg": "x", "assets/big.psd": "x"},
			want:  []string{".dockerignore", "assets/logo.svg"},
		},
		{
			name: "lockfiles and ocel config always go up",
			files: map[string]string{
				".gitignore":     "*.lock\n*.yaml\n*.json\ngo.sum\n",
				"pnpm-lock.yaml": "x",
				"yarn.lock":      "x",
				"Cargo.lock":     "x",
				"go.sum":         "x",
				"uv.lock":        "x",
				"ocel.json":      "{}",
				"ocel.prod.json": "{}",
				"other.json":     "{}",
			},
			want: []string{".gitignore", "Cargo.lock", "go.sum", "ocel.json", "ocel.prod.json", "pnpm-lock.yaml", "uv.lock", "yarn.lock"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, tt.files)
			archive, err := sourcearchive.Walk(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			if got := paths(archive); !slices.Equal(got, tt.want) {
				t.Fatalf("uploaded %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWalkRecordsEachFileByDigestAndOpensItsBytes(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"a.txt": "same", "b/c.txt": "same", "d.txt": "different"})
	archive, err := sourcearchive.Walk(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	want := []*cloudv1.SourceFile{
		{Path: "a.txt", Digest: sum("same"), Size: 4},
		{Path: "b/c.txt", Digest: sum("same"), Size: 4},
		{Path: "d.txt", Digest: sum("different"), Size: 9},
	}
	for i, file := range archive.Manifest.GetFiles() {
		if file.GetPath() != want[i].Path || file.GetDigest() != want[i].Digest || file.GetSize() != want[i].Size {
			t.Fatalf("file %d = %v, want %v", i, file, want[i])
		}
	}
	blobs := archive.Blobs()
	if len(blobs) != 2 {
		t.Fatalf("blobs = %v, want the two distinct contents once each", blobs)
	}
	for _, blob := range blobs {
		opened, err := archive.Open(blob.Digest)
		if err != nil {
			t.Fatal(err)
		}
		read, err := io.ReadAll(opened)
		opened.Close()
		if err != nil {
			t.Fatal(err)
		}
		if sum(string(read)) != blob.Digest || uint64(len(read)) != blob.Size {
			t.Fatalf("blob %s opened %q", blob.Digest, read)
		}
	}
	if _, err := archive.Open(sum("absent")); err == nil {
		t.Fatal("opened a digest the archive does not hold")
	}
}

func TestWalkRecordsTheExecutableBit(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows has no executable bit")
	}
	root := t.TempDir()
	write(t, root, map[string]string{"run.sh": "#!/bin/sh", "data.txt": "x"})
	if err := os.Chmod(filepath.Join(root, "run.sh"), 0o755); err != nil {
		t.Fatal(err)
	}
	archive, err := sourcearchive.Walk(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	executable := map[string]bool{}
	for _, f := range archive.Manifest.GetFiles() {
		executable[f.GetPath()] = f.GetExecutable()
	}
	if !executable["run.sh"] || executable["data.txt"] {
		t.Fatalf("executable = %v, want only run.sh", executable)
	}
}

func TestWalkRecordsASymlinkInsideTheRootAsASymlink(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX symlinks")
	}
	root := t.TempDir()
	write(t, root, map[string]string{"apps/web/index.ts": "x"})
	if err := os.Symlink("apps/web", filepath.Join(root, "current")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(root, "apps", "web", "index.ts"), filepath.Join(root, "apps", "entry.ts")); err != nil {
		t.Fatal(err)
	}
	archive, err := sourcearchive.Walk(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	targets := map[string]string{}
	for _, f := range archive.Manifest.GetFiles() {
		if f.GetSymlinkTarget() != "" {
			if f.GetDigest() != "" || f.GetSize() != 0 {
				t.Fatalf("symlink %s carries content: %v", f.GetPath(), f)
			}
			targets[f.GetPath()] = f.GetSymlinkTarget()
		}
	}
	if targets["current"] != "apps/web" || targets["apps/entry.ts"] != "web/index.ts" || len(targets) != 2 {
		t.Fatalf("symlinks = %v, want current → apps/web and apps/entry.ts → web/index.ts", targets)
	}
	if got := paths(archive); slices.Contains(got, "current/index.ts") {
		t.Fatalf("walked through the symlinked directory: %q", got)
	}
}

func TestWalkRefusesASymlinkThatEscapesTheRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX symlinks")
	}
	for name, target := range map[string]string{
		"relative": "../../outside",
		"absolute": "/etc/passwd",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, map[string]string{"src/a.ts": "x"})
			if err := os.Symlink(target, filepath.Join(root, "src", "link")); err != nil {
				t.Fatal(err)
			}
			_, err := sourcearchive.Walk(context.Background(), root)
			if err == nil || !strings.Contains(err.Error(), "src/link") || !strings.Contains(err.Error(), "outside") {
				t.Fatalf("err = %v, want a refusal naming src/link", err)
			}
		})
	}
}

func TestWalkSkipsAnIgnoredSymlinkThatEscapesTheRoot(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses POSIX symlinks")
	}
	root := t.TempDir()
	write(t, root, map[string]string{".gitignore": "link\n", "a.ts": "x"})
	if err := os.Symlink("/etc", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	archive, err := sourcearchive.Walk(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(archive); !slices.Equal(got, []string{".gitignore", "a.ts"}) {
		t.Fatalf("uploaded %q", got)
	}
}

func sparse(t *testing.T, file string, size int64) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		t.Fatal(err)
	}
	created, err := os.Create(file)
	if err != nil {
		t.Fatal(err)
	}
	if err := created.Truncate(size); err != nil {
		t.Fatal(err)
	}
	created.Close()
}

func TestWalkRefusesAFileOverTheCeilingAndNamesIt(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"a.ts": "x"})
	sparse(t, filepath.Join(root, "data", "dump.sql"), sourcearchive.MaxFileBytes+1)
	_, err := sourcearchive.Walk(context.Background(), root)
	if refusalCode(err) != clierror.CodeCloudSourceInvalid || !strings.Contains(err.Error(), "data/dump.sql") || !strings.Contains(err.Error(), "512.0 MiB") {
		t.Fatalf("err = %v, want a refusal naming data/dump.sql and the ceiling", err)
	}
}

func TestWalkRefusesASourceOverTheTotalCeilingAndNamesTheBiggestFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"a.ts": "x"})
	for i, name := range []string{"one.bin", "two.bin", "three.bin", "four.bin", "five.bin"} {
		sparse(t, filepath.Join(root, "blobs", name), sourcearchive.MaxFileBytes-int64(i))
	}
	_, err := sourcearchive.Walk(context.Background(), root)
	if refusalCode(err) != clierror.CodeCloudSourceInvalid || !strings.Contains(err.Error(), "2.0 GiB") || !strings.Contains(err.Error(), "blobs/one.bin") || strings.Contains(err.Error(), "a.ts") {
		t.Fatalf("err = %v, want a refusal naming the ceiling and the five biggest files", err)
	}
}

func TestWalkSkipsSpecialFiles(t *testing.T) {
	root := t.TempDir()
	write(t, root, map[string]string{"a.ts": "x"})
	if err := mkfifo(filepath.Join(root, "pipe")); err != nil {
		t.Skip("no mkfifo: ", err)
	}
	archive, err := sourcearchive.Walk(context.Background(), root)
	if err != nil {
		t.Fatal(err)
	}
	if got := paths(archive); !slices.Equal(got, []string{"a.ts"}) {
		t.Fatalf("uploaded %q, want only a.ts", got)
	}
}

func TestWalkHonoursTheUsersGlobalGitExcludes(t *testing.T) {
	for _, tt := range []struct {
		name   string
		config map[string]string
		xdg    bool
		files  map[string]string
		want   []string
	}{
		{
			name:   "the file core.excludesFile names",
			config: map[string]string{".gitconfig": "[core]\n\texcludesFile = ~/global-ignore\n", "global-ignore": "*.log\n"},
			xdg:    true,
			files:  map[string]string{"debug.log": "x", "main.go": "x"},
			want:   []string{"main.go"},
		},
		{
			name:   "git's ignore under XDG_CONFIG_HOME when core.excludesFile is unset",
			config: map[string]string{"xdg/git/ignore": "scratch/\n"},
			xdg:    true,
			files:  map[string]string{"scratch/a": "x", "main.go": "x"},
			want:   []string{"main.go"},
		},
		{
			name:   "git's ignore under ~/.config when XDG_CONFIG_HOME is unset too",
			config: map[string]string{".config/git/ignore": "scratch/\n"},
			files:  map[string]string{"scratch/a": "x", "main.go": "x"},
			want:   []string{"main.go"},
		},
		{
			name:   "a .gitignore includes again what the global excludes leave out",
			config: map[string]string{"xdg/git/ignore": "*.log\n"},
			xdg:    true,
			files:  map[string]string{".gitignore": "!keep.log\n", "keep.log": "x", "drop.log": "x"},
			want:   []string{".gitignore", "keep.log"},
		},
	} {
		t.Run(tt.name, func(t *testing.T) {
			home := t.TempDir()
			write(t, home, tt.config)
			t.Setenv("HOME", home)
			t.Setenv("XDG_CONFIG_HOME", "")
			if tt.xdg {
				t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "xdg"))
			}
			root := t.TempDir()
			write(t, root, tt.files)
			archive, err := sourcearchive.Walk(context.Background(), root)
			if err != nil {
				t.Fatal(err)
			}
			if got := paths(archive); !slices.Equal(got, tt.want) {
				t.Fatalf("uploaded %q, want %q", got, tt.want)
			}
		})
	}
}

func TestWalkRefusesWhenGitCannotReadItsOwnConfig(t *testing.T) {
	home := t.TempDir()
	write(t, home, map[string]string{".gitconfig": "[core\n"})
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", "")
	root := t.TempDir()
	write(t, root, map[string]string{"main.go": "x"})
	_, err := sourcearchive.Walk(context.Background(), root)
	if err == nil || !strings.Contains(err.Error(), "core.excludesFile") || !strings.Contains(err.Error(), "bad config") {
		t.Fatalf("err = %v, want git's own complaint about its config", err)
	}
}

func refusalCode(err error) string {
	var coded *clierror.Error
	if errors.As(err, &coded) {
		return coded.Code
	}
	return ""
}

func TestWalkRefusesASourceOcelCloudWouldNotAcceptBeforeAnyUpload(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("windows refuses these file names")
	}
	for _, tt := range []struct {
		name  string
		files map[string]string
		names string
	}{
		{name: "a path with a backslash", files: map[string]string{`src/a\b.ts`: "x", "main.ts": "x"}, names: `src/a\\b.ts`},
		{name: "a path with a control character", files: map[string]string{"notes\x01.txt": "x"}, names: `notes\x01.txt`},
		{name: "no file at all", files: map[string]string{".env": "SECRET=1"}, names: "0 files"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, tt.files)
			_, err := sourcearchive.Walk(context.Background(), root)
			if code := refusalCode(err); code != clierror.CodeCloudSourceInvalid {
				t.Fatalf("err = %v (code %q), want %s", err, code, clierror.CodeCloudSourceInvalid)
			}
			if !strings.Contains(err.Error(), tt.names) {
				t.Fatalf("err = %v, want it to name %s", err, tt.names)
			}
		})
	}
}
