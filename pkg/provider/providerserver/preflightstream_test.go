package providerserver_test

import (
	"context"
	"errors"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
)

func preflight(ctx context.Context, client contractv1connect.ProviderServiceClient, req *contractv1.PreflightRequest) (*contractv1.PreflightResponse, error) {
	resp, _, err := preflightWithSteps(ctx, client, req)
	return resp, err
}

func preflightWithSteps(ctx context.Context, client contractv1connect.ProviderServiceClient, req *contractv1.PreflightRequest) (*contractv1.PreflightResponse, []*progressv1.OperationEvent, error) {
	stream, err := client.Preflight(ctx, req)
	if err != nil {
		return nil, nil, err
	}
	defer stream.Close()
	var events []*progressv1.OperationEvent
	var resp *contractv1.PreflightResponse
	for stream.Receive() {
		if got := stream.Msg().GetResponse(); got != nil {
			resp = got
			continue
		}
		events = append(events, stream.Msg().GetProgress())
	}
	if err := stream.Err(); err != nil {
		return nil, events, err
	}
	if resp == nil {
		return nil, events, errors.New("the Preflight stream closed without a response")
	}
	return resp, events, nil
}
