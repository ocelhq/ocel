package fake

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	ProgramStore = "fake-deployments-store"

	ProgramEdgeVar    = "OCEL_EDGE"
	ProgramPreviewVar = "OCEL_PREVIEW_BASE_DOMAIN"
)

func (p *Provider) ProgramEdge(_ context.Context, req provider.EdgeProgramRequest) (provider.EdgeProgram, error) {
	variables := map[string]string{ProgramEdgeVar: string(req.Kind)}
	if req.PreviewBaseDomain != "" {
		variables[ProgramPreviewVar] = req.PreviewBaseDomain
	}
	worker := edge.Worker{Main: req.Entry, Variables: variables}
	if req.PreviewKey != "" {
		worker.Secrets = map[string]string{edge.PreviewKeyVar: string(req.PreviewKey)}
	}
	return provider.EdgeProgram{
		Spec: &edge.ProgramSpec{
			Name:            ProgramName(req.Slug, req.Tier),
			Worker:          worker,
			StoreScriptName: ProgramStore,
		},
		Values: map[string]string{ProgramEdgeVar: string(req.Kind)},
	}, nil
}

func ProgramName(slug string, tier environment.Tier) string {
	if slug == "" {
		return ""
	}
	return strings.Join([]string{"fake", slug, string(tier)}, "--")
}
