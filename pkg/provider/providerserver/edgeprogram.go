package providerserver

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

type edgeProgramScope struct {
	tier              environment.Tier
	env               string
	previewBaseDomain string
	previewKey        edge.PreviewKey
}

func edgeProgramFor(ctx context.Context, p provider.Provider, front edge.Edge, scope edgeProgramScope) (*edge.Program, error) {
	if !front.Facts().RunsCode {
		return nil, nil
	}
	program := p.Hooks().ProgramEdge
	if program == nil {
		return nil, refusal.Refuse(refusal.CodeNotReady,
			"the %s edge answers every request from an entry worker it runs, and this provider builds no program for that worker to run: "+
				"raising the surface here would leave nothing behind it, so deploy through an edge this provider programs instead",
			front.Kind())
	}
	built, err := program(ctx, provider.EdgeProgramRequest{Tier: scope.tier, Kind: front.Kind()})
	if err != nil {
		return nil, err
	}
	if err := built.Origin.RefuseIncomplete(); err != nil {
		return nil, refusal.Refuse(refusal.CodeNotReady, "the %s edge cannot reach this origin: %v", front.Kind(), err)
	}
	built.Env = scope.env
	built.PreviewBaseDomain = scope.previewBaseDomain
	built.PreviewKey = scope.previewKey
	return &built, nil
}
