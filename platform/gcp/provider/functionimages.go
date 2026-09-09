package gcp

import (
	"context"
	"maps"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/google"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/gcp/provider/payloads"
)

const (
	nodeImage   = "gcr.io/distroless/nodejs24-debian12@sha256:61f4f4341db81820c24ce771b83d202eb6452076f58628cd536cc7d94a10978b"
	staticImage = "gcr.io/distroless/static-debian12@sha256:d75cdd72874d4790092fcb1b058493ecf6bb5bf2b2b897045b00ff01d91843f2"
	pythonImage = "gcr.io/distroless/python3-debian13@sha256:f2b206661cee3edb44f132d7f054a9ced96f671d8a973de0db750895c9acb2fb"
)

const nodeBinDir = "/nodejs/bin"

const pathVariable = "PATH"

type base struct {
	ref  string
	bins []string
}

func functionBases() map[string]base {
	return map[string]base{
		providerkit.RuntimeNode:   {ref: nodeImage, bins: []string{nodeBinDir}},
		providerkit.RuntimeGo:     {ref: staticImage},
		providerkit.RuntimePython: {ref: pythonImage},
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

func (p *Provider) ServesFunctionURLs() bool { return true }

func (p *Provider) FunctionBase(ctx context.Context, runtime providerkit.Runtime) (v1.Image, error) {
	if err := runsX8664(runtime.Arch, "the "+runtime.Name+" function"); err != nil {
		return nil, err
	}
	on, carried := p.bases[runtime.Name]
	if !carried {
		return nil, providerkit.Refuse(providerkit.CodeInvalid,
			"a function on Cloud Run is a container, and this provider carries no base image a %s function could run in: it carries one for %s",
			runtime.Name, strings.Join(slices.Sorted(maps.Keys(p.bases)), ", "))
	}
	image, err := p.based(ctx, on.ref)
	if err != nil {
		return nil, err
	}
	return commandable(image, on.bins)
}

func (p *Provider) based(ctx context.Context, ref string) (v1.Image, error) {
	held, _ := p.pulled.LoadOrStore(ref, &memo[v1.Image]{})
	return held.(*memo[v1.Image]).held(func() (v1.Image, error) { return p.pull(ctx, ref) })
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
	held := strings.Join(bins, ":")
	for _, entry := range env {
		named, value, _ := strings.Cut(entry, "=")
		if named != pathVariable {
			kept = append(kept, entry)
			continue
		}
		held = strings.Join(append(slices.Clone(bins), value), ":")
	}
	return append(kept, pathVariable+"="+held)
}

func (p *Provider) FunctionRuntimePayload(_ context.Context, runtime providerkit.Runtime) ([]byte, error) {
	if !providerkit.BootsThroughRuntime(runtime) {
		return nil, nil
	}
	return payloads.NodeRuntime(), nil
}

func runsX8664(arch, what string) error {
	if providerkit.Architecture(arch) == providerkit.ArchX8664 {
		return nil
	}
	return providerkit.Refuse(providerkit.CodeInvalid,
		"%s is built for %s, and Cloud Run runs %s alone: build it for %s, or run it somewhere that offers %s",
		what, providerkit.Architecture(arch), providerkit.ArchX8664, providerkit.ArchX8664, providerkit.Architecture(arch))
}

var (
	_ providerkit.FunctionImager     = (*Provider)(nil)
	_ providerkit.ServesFunctionURLs = (*Provider)(nil)
)
