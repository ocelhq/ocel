package providerprocess

import (
	"context"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func ForwardPorts(ctx context.Context, p *Provider, req *contractv1.ForwardPortsRequest, onResponse func(*contractv1.ForwardPortsResponse)) error {
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
		onResponse(stream.Msg())
	}
	if err := stream.Err(); err != nil {
		return p.process.streamError("ForwardPorts", err, false)
	}
	return nil
}
