package providerkit

import (
	"context"
	"fmt"

	connect "connectrpc.com/connect"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func (h *handlers) unlifted(rpc string) error {
	if _, err := h.session.use(); err != nil {
		return err
	}
	return connect.NewError(connect.CodeUnimplemented, fmt.Errorf("providerkit: %s is not lifted yet", rpc))
}

func (h *handlers) Configure(ctx context.Context, req *contractv1.ConfigureRequest) (*contractv1.ConfigureResponse, error) {
	if err := h.session.configure(ctx, Options(req.GetConfig().GetOptions().AsMap())); err != nil {
		return nil, err
	}
	return &contractv1.ConfigureResponse{}, nil
}

func (h *handlers) Deploy(context.Context, *contractv1.DeployRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return h.unlifted("Deploy")
}

func (h *handlers) RemoveProject(context.Context, *contractv1.ProjectRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return h.unlifted("RemoveProject")
}

func (h *handlers) PlanRemoveProject(context.Context, *contractv1.ProjectRequest) (*contractv1.RemovalPlan, error) {
	return nil, h.unlifted("PlanRemoveProject")
}
