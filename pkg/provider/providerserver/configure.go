package providerserver

import (
	"context"

	"github.com/ocelhq/ocel/pkg/edge"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/router"
)

func (h *handlers) Configure(ctx context.Context, req *contractv1.ConfigureRequest) (*contractv1.ConfigureResponse, error) {
	settings := provider.Settings{
		Options:    provider.Options(req.GetConfig().GetOptions().AsMap()),
		Transforms: req.GetConfig().GetTransforms(),
		Slug:       req.GetConfig().GetSlug(),
		ProjectDir: req.GetConfig().GetProjectDir(),
		WrittenBy:  h.session.writer,
	}
	if err := h.session.configure(ctx, settings); err != nil {
		return nil, err
	}
	p, err := h.session.use()
	if err != nil {
		return nil, err
	}
	front, err := h.edgeFor(p, req.GetEdge())
	if err != nil {
		return nil, err
	}
	facts := factsProto(p)
	facts.Edge = edgeFactsProto(front.Facts())
	dispatched, err := ListDispatchedComputes(p, front)
	if err != nil {
		return nil, err
	}
	facts.Edge.DispatchedComputes = provider.ComputeNames(dispatched)
	return &contractv1.ConfigureResponse{Facts: facts}, nil
}

func factsProto(p provider.Provider) *contractv1.ProviderFacts {
	facts := p.Facts()
	return &contractv1.ProviderFacts{
		PricesDeploys:    p.Hooks().Cost != nil,
		Computes:         provider.ComputeNames(facts.Computes),
		WorkerCeilings:   provider.WorkerCeilingMessages(facts.WorkerCeilings),
		MaxFunctionBytes: facts.MaxFunctionBytes,
		ForwardsPorts:    p.Hooks().BindingAccess != nil,
	}
}

func edgeFactsProto(facts edge.Facts) *contractv1.EdgeFacts {
	limits := facts.CacheTagLimits
	return &contractv1.EdgeFacts{
		RunsCode: facts.RunsCode,
		CacheTagLimits: &contractv1.CacheTagLimits{
			TagBytes:    int64(limits.TagBytes),
			Tags:        int64(limits.Tags),
			HeaderBytes: int64(limits.HeaderBytes),
		},
	}
}

func ListDispatchedComputes(p provider.Provider, front edge.Edge) ([]provider.Compute, error) {
	if !front.Facts().RunsCode {
		return nil, nil
	}
	var dispatched []provider.Compute
	for _, compute := range p.Facts().Computes {
		opened, paired, err := OpenDispatchingRouter(p, front, compute)
		if err != nil {
			return nil, err
		}
		if paired && opened.Facts().Dispatches {
			dispatched = append(dispatched, compute)
		}
	}
	return dispatched, nil
}

func OpenDispatchingRouter(p provider.Provider, front edge.Edge, compute provider.Compute) (router.Router, bool, error) {
	kind, paired := readPairingFacts(p, front).PairedRouter(front.Kind(), compute)
	if !paired {
		return nil, false, nil
	}
	if by, fronted := listFrontingRouters(p.Facts(), front)[kind]; fronted {
		kind = by
	}
	opened, err := p.Routers().Open(kind)
	return opened, err == nil, err
}
