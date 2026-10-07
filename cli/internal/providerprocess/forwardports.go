package providerprocess

import (
	"context"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func ForwardPorts(ctx context.Context, p *Provider, req *contractv1.ForwardPortsRequest,
	onResponse func(*contractv1.ForwardPortsResponse), onProgress func(*progressv1.OperationEvent),
) error {
	client, err := p.process.Client()
	if err != nil {
		return err
	}
	stream, err := client.ForwardPorts(ctx, req)
	if err != nil {
		return p.process.callError("ForwardPorts", err)
	}
	defer stream.Close()
	for stream.Receive() {
		if response := stream.Msg().GetResponse(); response != nil {
			onResponse(response)
			continue
		}
		onProgress(stream.Msg().GetProgress())
	}
	if err := stream.Err(); err != nil {
		return p.process.streamError("ForwardPorts", err, false)
	}
	return nil
}
