package provider

import (
	"context"

	v1 "github.com/google/go-containerregistry/pkg/v1"
)

type Runtime interface {
	Arch(ctx context.Context, app, declared string) (string, error)
	Binary(ctx context.Context, arch string) ([]byte, error)
}

type Wrapped func(ctx context.Context) (v1.Image, func(), error)
