package deploy

import (
	"context"

	"github.com/ocelhq/ocel/pkg/naming"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func recordInfraStack(ctx context.Context, cfg Config, stack naming.StackName, links []*linksv1.Link) error {
	if cfg.Records == nil {
		return nil
	}
	return providerkit.WriteStack(ctx, cfg.Records, cfg.Slug, stack, providerkit.Stack{
		Kind:   providerkit.StackInfra,
		Links:  recordedLinks(links),
		Writer: providerkit.WriterFor(""),
	})
}

func recordAppStack(ctx context.Context, cfg Config, stack naming.StackName, app string, id Identity, outputs []*progressv1.FunctionOutput, names map[string]string) error {
	if cfg.Records == nil {
		return nil
	}
	functions := make([]providerkit.Function, 0, len(outputs))
	for _, out := range outputs {
		functions = append(functions, providerkit.Function{
			Name:     out.GetLogicalName(),
			Physical: names[out.GetLogicalName()],
			URL:      out.GetUrl(),
		})
	}
	return providerkit.WriteStack(ctx, cfg.Records, cfg.Slug, stack, providerkit.Stack{
		Kind:      providerkit.StackApp,
		App:       app,
		Release:   releaseOf(id).String(),
		Identity:  id.String(),
		Functions: functions,
		Writer:    providerkit.WriterFor(""),
	})
}

func recordedLinks(links []*linksv1.Link) []providerkit.Link {
	if len(links) == 0 {
		return nil
	}
	out := make([]providerkit.Link, 0, len(links))
	for _, link := range links {
		kind := providerkit.LinkCustom
		switch {
		case link.GetPostgres() != nil:
			kind = providerkit.LinkPostgres
		case link.GetBucket() != nil:
			kind = providerkit.LinkBucket
		}
		out = append(out, linkOf(kind, link))
	}
	return out
}
