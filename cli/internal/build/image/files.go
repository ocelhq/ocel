package image

import (
	"context"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"

	"github.com/moby/buildkit/client"
	"github.com/moby/buildkit/exporter/containerimage/exptypes"
	"github.com/tonistiigi/fsutil"

	"github.com/ocelhq/ocel/pkg/images"
)

const filesDockerfile = "Dockerfile"

func AddFiles(ctx context.Context, base Image, slug, app, files, dst, arch string, progress io.Writer) (Image, error) {
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

	frontend, err := os.MkdirTemp("", "ocel-files-")
	if err != nil {
		return Image{}, err
	}
	defer func() { _ = os.RemoveAll(frontend) }()
	dockerfile := "FROM " + base.Repository + ":" + base.Tag + "\nCOPY . " + strconv.Quote(dst+"/") + "\n"
	if err := os.WriteFile(filepath.Join(frontend, filesDockerfile), []byte(dockerfile), 0o600); err != nil {
		return Image{}, err
	}
	source, err := fsutil.NewFS(files)
	if err != nil {
		return Image{}, fmt.Errorf("read %s as what %s's image gains: %w", files, app, err)
	}
	plan, err := fsutil.NewFS(frontend)
	if err != nil {
		return Image{}, err
	}
	opt := client.SolveOpt{
		Frontend:      dockerfileFrontend,
		FrontendAttrs: map[string]string{filenameAttr: filesDockerfile},
		LocalMounts:   map[string]fsutil.FS{contextMount: source, frontendMount: plan},
		Exports:       exports(),
	}
	if arch != "" {
		opt.FrontendAttrs[platformAttr] = images.ContainerPlatform(arch)
	}

	if progress != nil {
		progress = &lockedWriter{w: progress}
	}
	status := make(chan *client.SolveStatus)
	reported := make(chan struct{})
	go func() {
		defer close(reported)
		report(status, progress)
	}()
	resp, err := buildkit.Solve(ctx, nil, opt, status)
	<-reported
	if err != nil {
		return Image{}, fmt.Errorf("add %s to %s's image: %w", dst, app, err)
	}
	built, err := imageFor(slug, app, resp.ExporterResponse[exptypes.ExporterImageDigestKey])
	if err != nil {
		return Image{}, err
	}
	if err := d.tag(ctx, built); err != nil {
		return Image{}, err
	}
	return built, nil
}
