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

	ProgramEdgeVar        = "OCEL_EDGE"
	ProgramPreviewVar     = "OCEL_PREVIEW_BASE_DOMAIN"
	ProgramPreviewAppsVar = "OCEL_PREVIEW_APPS"
)

func (p *Provider) ProgramEdge(_ context.Context, req provider.EdgeProgramRequest) (provider.EdgeProgram, error) {
	vars := map[string]string{ProgramEdgeVar: string(req.Kind)}
	if req.PreviewBaseDomain != "" {
		vars[ProgramPreviewVar] = req.PreviewBaseDomain
	}
	if len(req.Apps) > 0 {
		vars[ProgramPreviewAppsVar] = strings.Join(req.Apps, ",")
	}
	return provider.EdgeProgram{
		Spec: &edge.ProgramSpec{
			Name: ProgramName(req.Slug, req.Tier),
			Worker: edge.Worker{
				Main: req.Entry,
				Vars: vars,
			},
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
