package providerserver

import (
	"context"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
)

func (h *handlers) Configure(ctx context.Context, req *contractv1.ConfigureRequest) (*contractv1.ConfigureResponse, error) {
	settings := provider.Settings{
		Options:    provider.Options(req.GetConfig().GetOptions().AsMap()),
		Transforms: req.GetConfig().GetTransforms(),
		Slug:       req.GetConfig().GetSlug(),
		ProjectDir: req.GetConfig().GetProjectDir(),
	}
	if err := h.session.configure(ctx, settings); err != nil {
		return nil, err
	}
	p, err := h.session.use()
	if err != nil {
		return nil, err
	}
	return &contractv1.ConfigureResponse{Facts: factsProto(p)}, nil
}

func factsProto(p provider.Provider) *contractv1.ProviderFacts {
	facts := p.Facts()
	return &contractv1.ProviderFacts{
		PricesDeploys:          p.Hooks().Cost != nil,
		Computes:               provider.ComputeNames(facts.Computes),
		WorkerCeilings:         provider.WorkerCeilingMessages(facts.WorkerCeilings),
		NextRuntimeDir:         facts.NextRuntimeDir,
		MaxFunctionBytes:       facts.MaxFunctionBytes,
		NextRefreshesByRequest: facts.NextRefreshesByRequest,
	}
}
