package image

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	railpack "github.com/railwayapp/railpack/buildkit"
	"github.com/tonistiigi/fsutil"

	"github.com/ocelhq/ocel/pkg/images"
)

const (
	contextMount  = "context"
	frontendMount = "dockerfile"
	mobyExporter  = "moby"

	dockerfileFrontend = "dockerfile.v0"
	filenameAttr       = "filename"
	platformAttr       = "platform"
)

func Build(ctx context.Context, app App, arch string, progress io.Writer) (Image, error) {
	recipe, err := ChooseRecipe(app)
	if err != nil {
		return Image{}, err
	}
	d, err := openDaemon()
	if err != nil {
		return Image{}, err
	}
	buildkit, err := d.buildkit(ctx)
	if err != nil {
		return Image{}, d.unreachable(err)
	}
	defer func() { _ = buildkit.Close() }()

	if err := d.usable(ctx, buildkit, arch); err != nil {
		return Image{}, err
	}

	opt, done, err := recipe.solve(arch)
	if err != nil {
		return Image{}, err
	}
	defer done()

	status := make(chan *client.SolveStatus)
	reported := make(chan struct{})
	go func() {
		defer close(reported)
		report(status, progress)
	}()
	resp, err := recipe.run(ctx, buildkit, opt, status)
	<-reported
	if err != nil {
		return Image{}, fmt.Errorf("build %s: %w", app.Name, err)
	}

	image, err := imageFor(app.Slug, app.Name, resp.ExporterResponse[exptypes.ExporterImageDigestKey])
	if err != nil {
		return Image{}, err
	}
	if err := d.tag(ctx, image); err != nil {
		return Image{}, err
	}
	return image, nil
}

func (r Recipe) railpack() bool { return r.Dockerfile == "" }

func (r Recipe) solve(arch string) (client.SolveOpt, func(), error) {
	opt, done, err := r.unpinned()
	if err != nil || arch == "" {
		return opt, done, err
	}
	if opt.FrontendAttrs == nil {
		opt.FrontendAttrs = map[string]string{}
	}
	opt.FrontendAttrs[platformAttr] = images.ContainerPlatform(arch)
	return opt, done, nil
}

func (r Recipe) unpinned() (client.SolveOpt, func(), error) {
	if !r.railpack() {
		opt, err := dockerfileOptions(r.App.Workspace.Root, r.Dockerfile)
		return opt, func() {}, err
	}
	plan, err := Plan(r.App.Workspace)
	if err != nil {
		return client.SolveOpt{}, nil, err
	}
	planDir, err := stagePlan(plan)
	if err != nil {
		return client.SolveOpt{}, nil, err
	}
	opt, err := solveOptions(r.App.Workspace.Root, planDir)
	if err != nil {
		_ = os.RemoveAll(planDir)
		return client.SolveOpt{}, nil, err
	}
	return opt, func() { _ = os.RemoveAll(planDir) }, nil
}

func (r Recipe) run(ctx context.Context, buildkit *client.Client, opt client.SolveOpt, status chan *client.SolveStatus) (*client.SolveResponse, error) {
	if r.railpack() {
		return buildkit.Build(ctx, opt, "", railpack.Build, status)
	}
	return buildkit.Solve(ctx, nil, opt, status)
}

func stagePlan(plan []byte) (string, error) {
	dir, err := os.MkdirTemp("", "ocel-railpack-")
	if err != nil {
		return "", err
	}
	if err := os.WriteFile(filepath.Join(dir, PlanFileName), plan, 0o600); err != nil {
		_ = os.RemoveAll(dir)
		return "", err
	}
	return dir, nil
}

func solveOptions(root, planDir string) (client.SolveOpt, error) {
	source, err := contextFS(root)
	if err != nil {
		return client.SolveOpt{}, err
	}
	plan, err := fsutil.NewFS(planDir)
	if err != nil {
		return client.SolveOpt{}, err
	}
	return client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{contextMount: source, frontendMount: plan},
		Exports:     exports(),
	}, nil
}

func dockerfileOptions(root, dockerfile string) (client.SolveOpt, error) {
	source, err := contextFS(root)
	if err != nil {
		return client.SolveOpt{}, err
	}
	dockerfileDir, err := fsutil.NewFS(filepath.Dir(dockerfile))
	if err != nil {
		return client.SolveOpt{}, fmt.Errorf("read %s as the directory %s is in: %w", filepath.Dir(dockerfile), dockerfile, err)
	}
	return client.SolveOpt{
		Frontend:      dockerfileFrontend,
		FrontendAttrs: map[string]string{filenameAttr: filepath.Base(dockerfile)},
		LocalMounts:   map[string]fsutil.FS{contextMount: source, frontendMount: dockerfileDir},
		Exports:       exports(),
	}, nil
}

func exports() []client.ExportEntry {
	return []client.ExportEntry{{Type: mobyExporter}}
}

func report(status <-chan *client.SolveStatus, to io.Writer) {
	for update := range status {
		if to == nil {
			continue
		}
		for _, vertex := range update.Vertexes {
			if vertex.Completed != nil && vertex.Error == "" {
				fmt.Fprintln(to, vertex.Name)
			}
		}
		for _, line := range update.Logs {
			_, _ = to.Write(line.Data)
		}
	}
}
