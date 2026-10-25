package images

import (
	"context"
	"slices"
	"strings"

	"github.com/google/go-containerregistry/pkg/name"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/remote"
)

const NodeBase = "gcr.io/distroless/nodejs24-debian12@sha256:61f4f4341db81820c24ce771b83d202eb6452076f58628cd536cc7d94a10978b"

const NodeBinDir = "/nodejs/bin"

const pathVariable = "PATH"

func PullNodeBase(ctx context.Context, goarch string) (v1.Image, error) {
	pinned, err := name.NewDigest(NodeBase)
	if err != nil {
		return nil, err
	}
	image, err := remote.Image(pinned, remote.WithContext(ctx), remote.WithPlatform(v1.Platform{OS: "linux", Architecture: goarch}))
	if err != nil {
		return nil, err
	}
	return NewCommandImage(image, []string{NodeBinDir})
}

func NewCommandImage(image v1.Image, bins []string) (v1.Image, error) {
	file, err := image.ConfigFile()
	if err != nil {
		return nil, err
	}
	config := file.Config
	config.Entrypoint = nil
	config.Env = prependPath(config.Env, bins)
	return mutate.Config(image, config)
}

func prependPath(env []string, bins []string) []string {
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
