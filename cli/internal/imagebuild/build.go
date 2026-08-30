package imagebuild

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
)

const (
	contextMount = "context"
	planMount    = "dockerfile"
	mobyExporter = "moby"
)

type Builder struct {
	Progress io.Writer
}

func (b Builder) Build(ctx context.Context, app, appDir string) (Image, error) {
	d, err := openDaemon()
	if err != nil {
		return Image{}, err
	}
	builder, err := d.builder(ctx)
	if err != nil {
		return Image{}, d.unreachable(err)
	}
	defer func() { _ = builder.Close() }()

	if err := d.usable(ctx, builder); err != nil {
		return Image{}, err
	}

	plan, err := Plan(appDir)
	if err != nil {
		return Image{}, err
	}
	planDir, err := stagePlan(plan)
	if err != nil {
		return Image{}, err
	}
	defer func() { _ = os.RemoveAll(planDir) }()

	opt, err := solveOptions(appDir, planDir)
	if err != nil {
		return Image{}, err
	}

	status := make(chan *client.SolveStatus)
	reported := make(chan struct{})
	go func() {
		defer close(reported)
		report(status, b.Progress)
	}()
	resp, err := builder.Build(ctx, opt, "", railpack.Build, status)
	<-reported
	if err != nil {
		return Image{}, fmt.Errorf("build %s: %w", app, err)
	}

	image, err := imageFor(app, resp.ExporterResponse[exptypes.ExporterImageDigestKey])
	if err != nil {
		return Image{}, err
	}
	if err := d.tag(ctx, image); err != nil {
		return Image{}, err
	}
	return image, nil
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

func solveOptions(appDir, planDir string) (client.SolveOpt, error) {
	source, err := fsutil.NewFS(appDir)
	if err != nil {
		return client.SolveOpt{}, fmt.Errorf("read %s as a build context: %w", appDir, err)
	}
	plan, err := fsutil.NewFS(planDir)
	if err != nil {
		return client.SolveOpt{}, err
	}
	return client.SolveOpt{
		LocalMounts: map[string]fsutil.FS{contextMount: source, planMount: plan},
		Exports:     []client.ExportEntry{{Type: mobyExporter}},
	}, nil
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
