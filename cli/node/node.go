package node

import (
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"strings"
	"time"
)

//go:generate pnpm --dir ../.. exec turbo run build --filter=@cli/node

//go:embed dist
var embedded embed.FS

const (
	cacheDirName      = "node"
	variableEditorDir = "variable-editor"
	stagingSuffix     = ".staging-"
	unusedFor         = 30 * 24 * time.Hour
)

type Bundle struct {
	dir string
}

func (b Bundle) BuildScriptPath() string {
	return filepath.Join(b.dir, "build", "main.cjs")
}

func (b Bundle) DiscoveryRuntimePath() string {
	return filepath.Join(b.dir, "discovery", "runtime.mjs")
}

func (b Bundle) WorkerServerPath() string {
	return filepath.Join(b.dir, "worker", "server.mjs")
}

func (b Bundle) TracedWorkerServerPath() string {
	return filepath.Join(b.dir, "worker", "traced-server.mjs")
}

func (b Bundle) NextAdapterPath() string {
	return filepath.Join(b.dir, "next-adapter", "next-adapter.mjs")
}

func VariableEditor() (fs.FS, error) {
	return fs.Sub(embedded, path.Join("dist", variableEditorDir))
}

func Ensure() (Bundle, error) {
	base, err := os.UserCacheDir()
	if err != nil {
		return Bundle{}, fmt.Errorf("resolve the user cache directory for the node bundle: %w", err)
	}
	return ensureIn(filepath.Join(base, "ocel", cacheDirName), time.Now())
}

func ensureIn(cache string, now time.Time) (Bundle, error) {
	stamp, err := embedded.ReadFile("dist/STAMP")
	if err != nil {
		return Bundle{}, fmt.Errorf("read embedded node bundle stamp: %w", err)
	}
	name := strings.TrimSpace(string(stamp))
	if name == "" || strings.ContainsAny(name, `/\.`) {
		return Bundle{}, fmt.Errorf("the embedded node bundle stamp %q cannot name a directory", name)
	}
	dir := filepath.Join(cache, name)
	if info, err := os.Stat(dir); err == nil && info.IsDir() {
		_ = os.Chtimes(dir, now, now)
		return Bundle{dir: dir}, nil
	}

	if err := os.MkdirAll(cache, 0o755); err != nil {
		return Bundle{}, fmt.Errorf("create %s: %w", cache, err)
	}
	staging, err := os.MkdirTemp(cache, name+stagingSuffix)
	if err != nil {
		return Bundle{}, fmt.Errorf("stage the node bundle in %s: %w", cache, err)
	}
	defer os.RemoveAll(staging)
	if err := writeTree(staging); err != nil {
		return Bundle{}, err
	}
	if err := os.Rename(staging, dir); err != nil {
		if info, statErr := os.Stat(dir); statErr != nil || !info.IsDir() {
			return Bundle{}, fmt.Errorf("install the node bundle at %s: %w", dir, err)
		}
	}
	removeUnused(cache, name, now)
	return Bundle{dir: dir}, nil
}

func removeUnused(cache, current string, now time.Time) {
	entries, err := os.ReadDir(cache)
	if err != nil {
		return
	}
	for _, entry := range entries {
		if entry.Name() == current || !entry.IsDir() {
			continue
		}
		info, err := entry.Info()
		if err != nil || now.Sub(info.ModTime()) < unusedFor {
			continue
		}
		_ = os.RemoveAll(filepath.Join(cache, entry.Name()))
	}
}

func writeTree(dir string) error {
	tree, err := fs.Sub(embedded, "dist")
	if err != nil {
		return err
	}
	return fs.WalkDir(tree, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if name == variableEditorDir {
			return fs.SkipDir
		}
		out := filepath.Join(dir, filepath.FromSlash(name))
		if entry.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		content, err := fs.ReadFile(tree, name)
		if err != nil {
			return err
		}
		if err := os.WriteFile(out, content, 0o644); err != nil {
			return fmt.Errorf("write %s: %w", out, err)
		}
		return nil
	})
}
