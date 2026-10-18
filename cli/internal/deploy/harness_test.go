package deploy

import (
	"context"
	"io"
)

type deployOptions struct {
	yes      bool
	dry      bool
	tag      string
	prebuilt bool
}

func runDeploy(ctx context.Context, dependencies Dependencies, cwd string, opts deployOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	return To(ctx, dependencies, cwd, Production(opts.tag), Options{Yes: opts.yes, Dry: opts.dry, Prebuilt: opts.prebuilt}, stdout, stderr, stdin)
}

type previewUpOptions struct {
	name       string
	persistent bool
	prebuilt   bool
	yes        bool
	dry        bool
}

func (o previewUpOptions) target() Target {
	return Preview(o.name, o.persistent)
}

func runPreviewUp(ctx context.Context, dependencies Dependencies, cwd string, opts previewUpOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	return To(ctx, dependencies, cwd, opts.target(), Options{Yes: opts.yes, Dry: opts.dry, Prebuilt: opts.prebuilt}, stdout, stderr, stdin)
}

type previewRemoveOptions struct {
	name string
	yes  bool
}

func runPreviewRemove(ctx context.Context, dependencies Dependencies, cwd string, opts previewRemoveOptions, stdout, stderr io.Writer, stdin io.Reader) error {
	return RemovePreview(ctx, dependencies, cwd, opts.name, opts.yes, stdout, stderr, stdin)
}

type previewPruneOptions struct {
	name string
	keep int
}

func runPreviewPrune(ctx context.Context, dependencies Dependencies, cwd string, opts previewPruneOptions) error {
	return PrunePreview(ctx, dependencies, cwd, opts.name, opts.keep)
}
