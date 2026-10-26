package gcp

import (
	"context"
	"maps"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/transform"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/release"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/surface"
)

//go:generate go generate -C ../../../pkg/provider/transform ./...

func transformPass(root string, modules []string) transform.Pass {
	if len(modules) == 0 {
		return nil
	}
	return transform.NodePass{
		Root: root, Modules: modules,
		External:    []string{"@pulumi/*"},
		Uninstalled: "`@ocel/transforms` brings in `@pulumi/gcp` itself: install it as a devDependency and re-run.",
	}
}

func (p *Provider) evaluateTransforms(ctx context.Context, spec provider.StackSpec) (context.Context, error) {
	registry := p.components()
	var candidates []transform.Resource
	if spec.App == nil {
		candidates = registry.TransformCandidates(spec.Resources)
	} else {
		candidates = release.TransformCandidates(spec)
	}
	evaluated, err := surface.EvaluateStack(ctx, p.transforms, spec, candidates, p.transformed())
	if err != nil {
		return ctx, err
	}
	return surface.WithEvaluated(ctx, spec.Ref, evaluated), nil
}

func (p *Provider) transformed() map[string]surface.Allowed {
	allowed := p.components().Transformed()
	maps.Copy(allowed, release.Transformed())
	return allowed
}
