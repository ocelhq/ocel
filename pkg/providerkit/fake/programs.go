package fake

import (
	"context"
	"strings"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	ProgramStore  = "fake-deployments-store"
	ProgramSource = "the fake entry worker"

	ProgramEdgeVar        = "OCEL_EDGE"
	ProgramPreviewVar     = "OCEL_PREVIEW_BASE_DOMAIN"
	ProgramPreviewAppsVar = "OCEL_PREVIEW_APPS"
)

func (p *Provider) EdgeProgram(_ context.Context, req providerkit.EdgeProgramRequest) (providerkit.EdgeProgram, error) {
	vars := map[string]string{ProgramEdgeVar: string(req.Kind)}
	if req.PreviewBaseDomain != "" {
		vars[ProgramPreviewVar] = req.PreviewBaseDomain
	}
	if len(req.Apps) > 0 {
		vars[ProgramPreviewAppsVar] = strings.Join(req.Apps, ",")
	}
	return providerkit.EdgeProgram{
		Spec: &edge.ProgramSpec{
			Name: ProgramName(req.Slug, req.Class),
			Worker: edge.Worker{
				Main: edge.WorkerModule{
					Name:        "index.js",
					ContentType: "application/javascript+module",
					Content:     []byte(ProgramSource),
				},
				Vars: vars,
			},
			StoreScriptName: ProgramStore,
		},
		Values: map[string]string{ProgramEdgeVar: string(req.Kind)},
	}, nil
}

func ProgramName(slug string, class providerkit.Class) string {
	if slug == "" {
		return ""
	}
	return strings.Join([]string{"fake", slug, string(class)}, "--")
}
