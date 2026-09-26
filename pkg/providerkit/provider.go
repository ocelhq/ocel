package providerkit

import (
	"context"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func edgeProgramFor(ctx context.Context, p provider.Provider, front edge.Edge, req provider.EdgeProgramRequest) (provider.EdgeProgram, error) {
	if !front.Facts().RunsCode {
		return provider.EdgeProgram{}, nil
	}
	program := p.Hooks().ProgramEdge
	if program == nil {
		return provider.EdgeProgram{}, refusal.Refuse(refusal.CodeNotReady,
			"the %s edge answers every request from an entry worker it runs, and this provider builds no program for that worker to run: "+
				"raising the surface here would leave nothing behind it, so deploy through an edge this provider programs instead",
			front.Kind())
	}
	req.Kind = front.Kind()
	return program(ctx, req)
}
