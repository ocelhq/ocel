package deploy

import (
	"context"
	"io"
	"strings"
	"testing"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
)

type onStarted func(title string)

func (f onStarted) Receive(ev *streamv1.RunEvent) {
	if ev.GetOperation().GetStarted() != nil {
		f(ev.GetOperation().GetMessage())
	}
}

func (onStarted) Close() error { return nil }

func assertReportedUnder(t *testing.T, out, said, phase string) {
	t.Helper()
	for line := range strings.Lines(out) {
		if strings.Contains(line, said) {
			if !strings.Contains(line, "["+phase+"]") {
				t.Errorf("%q was reported as %q, want it under [%s]", said, line, phase)
			}
			return
		}
	}
	t.Errorf("stdout = %q, want %q reported under [%s]", out, said, phase)
}

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
	yes  bool
}

func runPreviewPrune(ctx context.Context, dependencies Dependencies, cwd string, opts previewPruneOptions, stdout io.Writer, stdin io.Reader) error {
	return PrunePreview(ctx, dependencies, cwd, opts.name, opts.keep, opts.yes, stdout, stdin)
}
