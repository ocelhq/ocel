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

func (h *handlers) Bootstrap(context.Context, *contractv1.BootstrapRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return h.unlifted("Bootstrap")
}

func (h *handlers) DescribeBootstrap(context.Context, *contractv1.DescribeBootstrapRequest) (*contractv1.DescribeBootstrapResponse, error) {
	return nil, h.unlifted("DescribeBootstrap")
}

func (h *handlers) GetCredentialPolicy(context.Context, *contractv1.CredentialPolicyRequest) (*contractv1.CredentialPolicyResponse, error) {
	return nil, h.unlifted("GetCredentialPolicy")
}

func (h *handlers) RemoveBootstrap(context.Context, *contractv1.BootstrapScope, *connect.ServerStream[progressv1.OperationEvent]) error {
	return h.unlifted("RemoveBootstrap")
}

func (h *handlers) PlanRemoveBootstrap(context.Context, *contractv1.BootstrapScope) (*contractv1.RemovalPlan, error) {
	return nil, h.unlifted("PlanRemoveBootstrap")
}

func (h *handlers) RemoveEnvironment(context.Context, *contractv1.RemoveEnvironmentRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return h.unlifted("RemoveEnvironment")
}

func (h *handlers) RemoveProject(context.Context, *contractv1.ProjectRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return h.unlifted("RemoveProject")
}

func (h *handlers) PlanRemoveProject(context.Context, *contractv1.ProjectRequest) (*contractv1.RemovalPlan, error) {
	return nil, h.unlifted("PlanRemoveProject")
}

func (h *handlers) ListEnvironments(context.Context, *contractv1.ListEnvironmentsRequest) (*contractv1.ListEnvironmentsResponse, error) {
	return nil, h.unlifted("ListEnvironments")
}

func (h *handlers) Preflight(context.Context, *contractv1.PreflightRequest) (*contractv1.PreflightResponse, error) {
	return nil, h.unlifted("Preflight")
}

func (h *handlers) ListPromotions(context.Context, *contractv1.ListPromotionsRequest) (*contractv1.ListPromotionsResponse, error) {
	return nil, h.unlifted("ListPromotions")
}

func (h *handlers) Rollback(context.Context, *contractv1.RollbackRequest) (*contractv1.RollbackResponse, error) {
	return nil, h.unlifted("Rollback")
}

func (h *handlers) RemoveStalePromotions(context.Context, *contractv1.RemoveStalePromotionsRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return h.unlifted("RemoveStalePromotions")
}

func (h *handlers) UsePreviewWildcard(context.Context, *contractv1.UsePreviewWildcardRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return h.unlifted("UsePreviewWildcard")
}

func (h *handlers) GetPreviewWildcard(context.Context, *contractv1.PreviewWildcardRequest) (*contractv1.GetPreviewWildcardResponse, error) {
	return nil, h.unlifted("GetPreviewWildcard")
}

func (h *handlers) PlanRemovePreviewWildcard(context.Context, *contractv1.PreviewWildcardRequest) (*contractv1.RemovalPlan, error) {
	return nil, h.unlifted("PlanRemovePreviewWildcard")
}

func (h *handlers) RemovePreviewWildcard(context.Context, *contractv1.PreviewWildcardRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return h.unlifted("RemovePreviewWildcard")
}

func (h *handlers) AddHostname(context.Context, *contractv1.HostnameRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return h.unlifted("AddHostname")
}

func (h *handlers) RemoveHostname(context.Context, *contractv1.HostnameRequest, *connect.ServerStream[progressv1.OperationEvent]) error {
	return h.unlifted("RemoveHostname")
}

func (h *handlers) GetHostnameStatus(context.Context, *contractv1.HostnameRequest) (*contractv1.GetHostnameStatusResponse, error) {
	return nil, h.unlifted("GetHostnameStatus")
}
