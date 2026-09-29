package providerserver

import (
	"context"
	"path/filepath"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func (h *handlers) Configure(ctx context.Context, req *contractv1.ConfigureRequest) (*contractv1.ConfigureResponse, error) {
	if dir := req.GetConfig().GetProjectDir(); !filepath.IsAbs(dir) {
		return nil, provider.RefusalError(refusal.Refuse(refusal.CodeInvalid,
			"the provider was configured with the project directory %q, and it reads the build under that directory: configure it with the project's absolute path", dir))
	}
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
	return &contractv1.ProviderFacts{PricesDeploys: p.Hooks().Cost != nil}
}
