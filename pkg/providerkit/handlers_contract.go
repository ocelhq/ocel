package providerkit

import (
	"context"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func (h *handlers) Configure(ctx context.Context, req *contractv1.ConfigureRequest) (*contractv1.ConfigureResponse, error) {
	if err := h.session.configure(ctx, Options(req.GetConfig().GetOptions().AsMap())); err != nil {
		return nil, err
	}
	return &contractv1.ConfigureResponse{}, nil
}
