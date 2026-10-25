package provider

import (
	"context"

	v1 "github.com/google/go-containerregistry/pkg/v1"

	"github.com/ocelhq/ocel/pkg/buildoutput"
)

type Runtime interface {
	Arch(ctx context.Context, app, declared string) (string, error)
	Binary(ctx context.Context, arch string) ([]byte, error)
	FunctionBase(ctx context.Context, framework buildoutput.Framework) (v1.Image, error)
	FunctionFiles(ctx context.Context, framework buildoutput.Framework) (map[string][]byte, error)
	FunctionBuildPaths(framework buildoutput.Framework) []string
}

type Wrapped func(ctx context.Context) (v1.Image, func(), error)
