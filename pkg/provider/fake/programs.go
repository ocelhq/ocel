package fake

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	ProgramStore = "fake-releases-store"

	ProgramEdgeVar    = "OCEL_EDGE"
	ProgramPreviewVar = "OCEL_PREVIEW_BASE_DOMAIN"
)

func (p *Provider) ProgramEdge(_ context.Context, req provider.EdgeProgramRequest) (provider.EdgeProgram, error) {
	variables := map[string]string{ProgramEdgeVar: string(req.Kind)}
	if req.PreviewBaseDomain != "" {
		variables[ProgramPreviewVar] = req.PreviewBaseDomain
	}
	secrets := map[string]string{}
	if req.PreviewKey != "" {
		secrets[edge.PreviewKeyVar] = string(req.PreviewKey)
	}
	worker := edge.Worker{Main: req.Entry, Variables: variables}
	if len(secrets) > 0 {
		worker.Secrets = secrets
	}
	return provider.EdgeProgram{
		Spec: &edge.ProgramSpec{
			Name:            ProgramName(req.Slug, req.Tier),
			Worker:          worker,
			StoreScriptName: ProgramStore,
		},
	}, nil
}

func ProgramName(slug string, tier environment.Tier) string {
	if slug == "" {
		return ""
	}
	return strings.Join([]string{"fake", slug, string(tier)}, "--")
}
