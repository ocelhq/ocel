package providerkit

import (
	"context"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func (h *handlers) Configure(ctx context.Context, req *contractv1.ConfigureRequest) (*contractv1.ConfigureResponse, error) {
	settings := Settings{
		Options:    Options(req.GetConfig().GetOptions().AsMap()),
		Transforms: req.GetConfig().GetTransforms(),
	}
	if err := h.session.configure(ctx, settings); err != nil {
		return nil, err
	}
	return &contractv1.ConfigureResponse{}, nil
}
