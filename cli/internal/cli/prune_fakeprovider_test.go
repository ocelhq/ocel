package cli

import (
	"context"

	connect "connectrpc.com/connect"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func (s *deployFakeProviderServer) Prune(ctx context.Context, req *deploymentsv1.PruneRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	if err := s.checkToken(ctx); err != nil {
		return err
	}

	var lines []string
	if req.GetKeepN() == 0 {
		lines = []string{"Nothing to prune."}
	} else {
		lines = []string{"Reclaimed 1 promotion(s): promo-1", "Kept 1 promotion(s)."}
	}
	for _, line := range lines {
		if err := stream.Send(&deploymentsv1.DeployEvent{
			Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: line}},
		}); err != nil {
			return err
		}
	}
	return stream.Send(&deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: true}},
	})
}
