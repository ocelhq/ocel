package clitest

import (
	"context"

	connect "connectrpc.com/connect"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func (s *deployFakeProviderServer) RemoveStalePromotions(ctx context.Context, req *contractv1.RemoveStalePromotionsRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	if err := declareFakeStages(stream); err != nil {
		return err
	}
	lines := []string{"PRUNE project=" + req.GetSlug() + " " + describeEnv(req.GetEnvironment())}
	if req.GetKeepN() == 0 {
		lines = append(lines, "Nothing to prune.")
	} else {
		lines = append(lines, "Reclaimed 1 promotion(s): promo-1", "Kept 1 promotion(s).")
	}
	for _, line := range lines {
		if err := stream.Send(fakeProgress(line)); err != nil {
			return err
		}
	}
	return stream.Send(&progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Result{Result: &progressv1.ResultEvent{Success: true}},
	})
}
