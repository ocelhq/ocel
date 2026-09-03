package node

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

//go:generate pnpm --dir ../.. exec turbo run build --filter=@cli/node

//go:embed dist
var embedded embed.FS

func DistDir(projectDir string) string {
	return filepath.Join(projectDir, ".ocel", "dist")
}

func BuilderPath(projectDir string) string {
	return filepath.Join(DistDir(projectDir), "builder", "cli.cjs")
}

func AdapterPath(projectDir string) string {
	return filepath.Join(DistDir(projectDir), "next-adapter", "next-adapter.mjs")
}

func WorkerBundles(projectDir string) map[string]string {
	return map[string]string{
		"cloudflare": filepath.Join(DistDir(projectDir), "workers", "entry-cloudflare.js"),
	}
}

func StoreWorkerBundles(projectDir string) map[string]string {
	return map[string]string{
		"cloudflare": filepath.Join(DistDir(projectDir), "workers", "store-cloudflare.js"),
	}
}

func ISRWriterBundles(projectDir string) map[string]string {
	return map[string]string{
		"cloudflare": filepath.Join(DistDir(projectDir), "workers", "isr-writer-cloudflare.js"),
	}
}

const varsUIDir = "vars-ui"

func VarsUI() (fs.FS, error) {
	return fs.Sub(embedded, path.Join("dist", varsUIDir))
}

func Ensure(projectDir string) error {
	want, err := embedded.ReadFile("dist/STAMP")
	if err != nil {
		return fmt.Errorf("read embedded node bundle stamp: %w", err)
	}

	dir := DistDir(projectDir)
	stampPath := filepath.Join(dir, "STAMP")
	if got, err := os.ReadFile(stampPath); err == nil && bytes.Equal(got, want) {
		return nil
	}

	if err := os.RemoveAll(dir); err != nil {
		return fmt.Errorf("clear %s: %w", dir, err)
	}
	if err := writeTree(dir); err != nil {
		return err
	}
	return os.WriteFile(stampPath, want, 0o644)
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
		if name == varsUIDir {
			return fs.SkipDir
		}
		out := filepath.Join(dir, filepath.FromSlash(name))
		if entry.IsDir() {
			return os.MkdirAll(out, 0o755)
		}
		if name == "STAMP" {
			return nil
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
