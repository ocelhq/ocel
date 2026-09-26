package providerserver

import (
	"context"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func (h *handlers) connector() (provider.Connector, error) {
	p, err := h.session.use()
	if err != nil {
		return nil, err
	}
	return p.Connector(), nil
}

func (h *handlers) DescribeConnectorTarget(ctx context.Context, _ *contractv1.DescribeConnectorTargetRequest) (*contractv1.DescribeConnectorTargetResponse, error) {
	connector, err := h.connector()
	if err != nil {
		return nil, err
	}
	described, err := connector.Target(ctx)
	if err != nil {
		return nil, provider.RefusalError(err)
	}
	resp := &contractv1.DescribeConnectorTargetResponse{
		TargetFingerprint: described.Fingerprint,
		Hostname:          described.Hostname,
		Arch:              described.Arch,
	}
	if described.Installed != nil {
		resp.Installed = &contractv1.InstalledConnector{
			Version:   described.Installed.Version,
			PublicKey: described.Installed.PublicKey,
			Compute:   string(described.Installed.Compute),
		}
	}
	return resp, nil
}

func (h *handlers) InstallConnector(ctx context.Context, req *contractv1.InstallConnectorRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	connector, err := h.connector()
	if err != nil {
		return err
	}
	return streamResult(ctx, stream, func(sender *eventStream) (*progressv1.OperationEvent, error) {
		sender.refusing(connect.CodeUnimplemented)
		var at provider.ConnectorAddress
		err := inUnit(sender, naming.UnitConnector, connectorUnitTitle, progressv1.Phase_PHASE_PROVISIONING, func(_ *eventStream, progress edge.Progress) error {
			at, err = connector.Install(ctx, provider.ConnectorInstall{
				Binary:  req.GetBinary(),
				Version: req.GetVersion(),
				Config:  req.GetConfigJson(),
				Compute: provider.Compute(req.GetCompute()),
			}, progress)
			return err
		})
		if err != nil {
			return nil, err
		}
		return connectorResult(at), nil
	})
}

func (h *handlers) RemoveConnector(ctx context.Context, _ *contractv1.RemoveConnectorRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	connector, err := h.connector()
	if err != nil {
		return err
	}
	return streamed(ctx, stream, naming.UnitConnector, connectorUnitTitle, progressv1.Phase_PHASE_DELETING, func(sender *eventStream, progress edge.Progress) error {
		sender.refusing(connect.CodeUnimplemented)
		return connector.Remove(ctx, progress)
	})
}

func connectorResult(at provider.ConnectorAddress) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Result{Result: &progressv1.ResultEvent{
			Success: true,
			Connector: &progressv1.ConnectorInstalled{
				Url:       at.URL,
				PublicKey: at.PublicKey,
				Compute:   string(at.Compute),
			},
		}},
	}
}
