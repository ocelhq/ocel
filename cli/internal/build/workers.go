package build

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/ocelhq/ocel/cli/internal/build/image"
	"github.com/ocelhq/ocel/cli/internal/build/toolchain"
	"github.com/ocelhq/ocel/cli/internal/discovery"
	"github.com/ocelhq/ocel/cli/internal/language"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/containerimage"
)

type HostedWorkers map[string][]string

func (t tools) addWorkerEntry(ctx context.Context, cfg *project.Project, app project.App, built image.Image, arch string, sources []string, log io.Writer) (image.Image, error) {
	roots, err := discovery.RootsOf(cfg)
	if err != nil {
		return image.Image{}, err
	}
	served, err := discovery.WorkerRoot(cfg.Dir, roots, app.Name, sources)
	if err != nil {
		return image.Image{}, err
	}
	if runs := language.OfApp(app.Framework(), filepath.Join(cfg.Dir, app.Path)); runs != "" && runs != served.Language {
		return image.Image{}, fmt.Errorf("app %q runs %s and the workers joining it serve what %s declares in %s: a worker runs in its app's image, so give the workers an app of their own in %s", app.Name, runs, served.Dir, served.Language, served.Language)
	}
	files, err := os.MkdirTemp("", "ocel-worker-entry-")
	if err != nil {
		return image.Image{}, err
	}
	defer func() { _ = os.RemoveAll(files) }()

	switch served.Language {
	case language.JS:
		if err := discovery.BundleNodeWorker(cfg.Dir, roots, filepath.Join(files, containerimage.NodeWorkerEntry)); err != nil {
			return image.Image{}, fmt.Errorf("app %q: %w", app.Name, err)
		}
	case language.Go:
		goarch, err := t.architecture(ctx, built.Repository, built.Digest)
		if err != nil {
			return image.Image{}, fmt.Errorf("read the architecture %s's image is built for: %w", app.Name, err)
		}
		moduleRoot, pkg, err := discovery.WriteGoWorkerEntry(cfg.Dir, roots, served)
		if err != nil {
			return image.Image{}, err
		}
		if err := toolchain.CompileGo(ctx, app.Name, moduleRoot, pkg, filepath.Join(files, buildoutput.GoWorkerBinary), goarch); err != nil {
			return image.Image{}, err
		}
	case language.Rust:
		return built, nil
	default:
		return image.Image{}, fmt.Errorf("app %q runs in an image and the workers joining it serve what %s declares in %s, and ocel builds no %s worker into an image yet: run the workers on a node, go or rust app", app.Name, served.Dir, served.Language, served.Language)
	}
	return t.addFiles(ctx, built, cfg.Slug, app.Name, files, containerimage.WorkerDir, arch, log)
}
