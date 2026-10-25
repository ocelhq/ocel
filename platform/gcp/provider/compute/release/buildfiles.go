package release

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/compute/next"
)

func (p *Release) UploadBuildFiles(ctx context.Context, spec provider.StackSpec, progress progress.Log) error {
	if next.Serves(spec.App) && spec.App.ISR != nil {
		if err := next.SeedPrerenders(ctx, p.Objects, spec, progress); err != nil {
			return err
		}
	}
	return p.Objects.UploadStaticFiles(ctx, spec, progress)
}
