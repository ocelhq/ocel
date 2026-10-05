package gcp

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"io/fs"
	"maps"
	"path"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/google"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads"
)

const (
	nodeImage   = "gcr.io/distroless/nodejs24-debian12@sha256:61f4f4341db81820c24ce771b83d202eb6452076f58628cd536cc7d94a10978b"
	staticImage = "gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2"
	pythonImage = "gcr.io/distroless/python3-debian13@sha256:f2b206661cee3edb44f132d7f054a9ced96f671d8a973de0db750895c9acb2fb"
)

const nodeBinDir = "/nodejs/bin"

const nextRuntimeDir = "/ocel/next"

const pathVariable = "PATH"

type base struct {
	ref     string
	bins    []string
	runtime *runtimeFiles
}

type runtimeFiles struct {
	dir   string
	files fs.FS
}

func functionBases() map[string]base {
	return map[string]base{
		buildoutput.FrameworkNode:   {ref: nodeImage, bins: []string{nodeBinDir}},
		buildoutput.FrameworkNext:   {ref: nodeImage, bins: []string{nodeBinDir}, runtime: &runtimeFiles{dir: nextRuntimeDir, files: payloads.NextRuntime()}},
		buildoutput.FrameworkGo:     {ref: staticImage},
		buildoutput.FrameworkPython: {ref: pythonImage},
		buildoutput.FrameworkRust:   {ref: staticImage},
	}
}

var runOn = v1.Platform{OS: "linux", Architecture: "amd64"}

func pullBase(ctx context.Context, ref string) (v1.Image, error) {
	pinned, err := name.NewDigest(ref)
	if err != nil {
		return nil, err
	}
	return remote.Image(pinned,
		remote.WithContext(ctx),
		remote.WithAuthFromKeychain(google.Keychain),
		remote.WithPlatform(runOn))
}

func (p *Provider) ResolveFunctionBase(ctx context.Context, framework buildoutput.Framework) (v1.Image, error) {
	if err := runsX8664(framework.Arch, "the "+framework.Name+" function"); err != nil {
		return nil, err
	}
	on, shipped := p.bases[framework.Name]
	if !shipped {
		return nil, refusal.Refuse(refusal.CodeInvalid,
			"a function on Cloud Run is a container, and this provider ships no base image a %s function could run in: it ships one for %s",
			framework.Name, strings.Join(slices.Sorted(maps.Keys(p.bases)), ", "))
	}
	image, err := p.based(ctx, on.ref)
	if err != nil {
		return nil, err
	}
	if on.runtime != nil {
		if image, err = appendRuntimeLayer(image, *on.runtime); err != nil {
			return nil, err
		}
	}
	return commandable(image, on.bins)
}

func appendRuntimeLayer(image v1.Image, runtime runtimeFiles) (v1.Image, error) {
	var packed bytes.Buffer
	archive := tar.NewWriter(&packed)
	err := fs.WalkDir(runtime.files, ".", func(name string, entry fs.DirEntry, err error) error {
		if err != nil || entry.IsDir() {
			return err
		}
		body, err := fs.ReadFile(runtime.files, name)
		if err != nil {
			return err
		}
		if err := archive.WriteHeader(&tar.Header{
			Typeflag: tar.TypeReg,
			Name:     strings.TrimPrefix(path.Join(runtime.dir, name), "/"),
			Mode:     0o644,
			Size:     int64(len(body)),
		}); err != nil {
			return err
		}
		_, err = archive.Write(body)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("pack the runtime files for %s: %w", runtime.dir, err)
	}
	if err := archive.Close(); err != nil {
		return nil, err
	}
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(packed.Bytes())), nil
	})
	if err != nil {
		return nil, err
	}
	return mutate.Append(image, mutate.Addendum{Layer: layer})
}

func (p *Provider) based(ctx context.Context, ref string) (v1.Image, error) {
	cached, _ := p.pulled.LoadOrStore(ref, &memo[v1.Image]{})
	return cached.(*memo[v1.Image]).get(func() (v1.Image, error) { return p.pull(ctx, ref) })
}

func commandable(image v1.Image, bins []string) (v1.Image, error) {
	file, err := image.ConfigFile()
	if err != nil {
		return nil, err
	}
	config := file.Config
	config.Entrypoint = nil
	config.Env = onPath(config.Env, bins)
	return mutate.Config(image, config)
}

func onPath(env []string, bins []string) []string {
	if len(bins) == 0 {
		return env
	}
	kept := make([]string, 0, len(env)+1)
	searchPath := strings.Join(bins, ":")
	for _, entry := range env {
		named, value, _ := strings.Cut(entry, "=")
		if named != pathVariable {
			kept = append(kept, entry)
			continue
		}
		searchPath = strings.Join(append(slices.Clone(bins), value), ":")
	}
	return append(kept, pathVariable+"="+searchPath)
}

func runsX8664(architecture, what string) error {
	if arch.Architecture(architecture) == arch.X8664 {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"%s is built for %s, and Cloud Run runs %s alone: build it for %s, or run it somewhere that offers %s",
		what, arch.Architecture(architecture), arch.X8664, arch.X8664, arch.Architecture(architecture))
}

func (p *Provider) ReadFunctionRuntime(_ context.Context, framework buildoutput.Framework) ([]byte, error) {
	if !images.BootsThroughNodeRuntime(framework) {
		return nil, nil
	}
	return payloads.NodeRuntime(), nil
}
