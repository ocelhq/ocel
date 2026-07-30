// Package platform carries the CLI's built JavaScript, embedded in the binary
// by one generate step. Most of it is the Node side — the app builder, the Next
// adapter and the edge worker bundles — materialized into a project's
// .ocel/dist on demand; callers resolve those through the path helpers here
// rather than stitching paths of their own. The variables UI is the exception:
// it is a browser page served straight out of the binary, so it is read as a
// file system and never written to disk.
package platform

import (
	"bytes"
	"embed"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
)

//go:generate sh ./generate.sh

//go:embed dist
var embedded embed.FS

// DistDir is where Ensure materializes the tree for a project.
func DistDir(projectDir string) string {
	return filepath.Join(projectDir, ".ocel", "dist")
}

// BuilderPath is the node entry that traces and bundles a project's apps.
func BuilderPath(projectDir string) string {
	return filepath.Join(DistDir(projectDir), "builder", "cli.cjs")
}

// AdapterPath is the Next build adapter, passed to `next build` as
// NEXT_ADAPTER_PATH. edge-cache-handler.cjs sits beside it, where the adapter
// resolves it relative to its own URL.
func AdapterPath(projectDir string) string {
	return filepath.Join(DistDir(projectDir), "next-runtime", "next-adapter.mjs")
}

// WorkerBundles maps framework to edge to bundle path. A pairing absent from it
// has no worker to deploy.
func WorkerBundles(projectDir string) map[string]map[string]string {
	return map[string]map[string]string{
		"next": {"cloudflare": filepath.Join(DistDir(projectDir), "workers", "next-cloudflare.js")},
	}
}

// StoreWorkerBundles maps edge to the deployments-store worker bundle — the root
// stack's own worker, one per edge rather than one per framework/edge pairing.
func StoreWorkerBundles(projectDir string) map[string]string {
	return map[string]string{
		"cloudflare": filepath.Join(DistDir(projectDir), "workers", "store-cloudflare.js"),
	}
}

// varsUIDir is the one subtree Ensure leaves behind: the variables UI is served
// from memory, so writing it into a project would put bytes on disk that
// nothing ever opens.
const varsUIDir = "vars-ui"

// VarsUI is the built variables UI, with index.html at its root.
func VarsUI() (fs.FS, error) {
	return fs.Sub(embedded, path.Join("dist", varsUIDir))
}

// Ensure materializes the embedded tree into projectDir, skipping the work when
// the on-disk STAMP already matches. STAMP is written last, so a run interrupted
// part way is redone rather than trusted.
func Ensure(projectDir string) error {
	want, err := embedded.ReadFile("dist/STAMP")
	if err != nil {
		return fmt.Errorf("read embedded platform stamp: %w", err)
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
