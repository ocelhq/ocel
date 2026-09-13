package providerkit

import (
	"context"
	"errors"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func (h *handlers) connectorHost() (ConnectorHost, error) {
	provider, err := h.session.use()
	if err != nil {
		return nil, err
	}
	installs, does := provider.(ConnectorHost)
	if !does {
		return nil, connect.NewError(connect.CodeUnimplemented,
			errors.New("this provider puts no connector on its targets; the console reaches a target of this kind through the provider itself"))
	}
	return installs, nil
}

func (h *handlers) DescribeConnectorTarget(ctx context.Context, _ *contractv1.DescribeConnectorTargetRequest) (*contractv1.DescribeConnectorTargetResponse, error) {
	installs, err := h.connectorHost()
	if err != nil {
		return nil, err
	}
	described, err := installs.DescribeConnectorTarget(ctx)
	if err != nil {
		return nil, RefusalError(err)
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
	installs, err := h.connectorHost()
	if err != nil {
		return err
	}
	return streamResult(ctx, stream, func(sender *eventSender) (*progressv1.OperationEvent, error) {
		var at ConnectorAddress
		err := inUnit(sender, naming.UnitConnector, connectorUnitTitle, progressv1.Phase_PHASE_PROVISIONING, func(_ *eventSender, report Reporter) error {
			at, err = installs.InstallConnector(ctx, ConnectorInstall{
				Binary:  req.GetBinary(),
				Version: req.GetVersion(),
				Config:  req.GetConfigJson(),
				Compute: Compute(req.GetCompute()),
			}, report)
			return err
		})
		if err != nil {
			return nil, err
		}
		return connectorResult(at), nil
	})
}

func (h *handlers) RemoveConnector(ctx context.Context, _ *contractv1.RemoveConnectorRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	installs, err := h.connectorHost()
	if err != nil {
		return err
	}
	return streamed(ctx, stream, naming.UnitConnector, connectorUnitTitle, progressv1.Phase_PHASE_DELETING, func(_ *eventSender, report Reporter) error {
		return installs.RemoveConnector(ctx, report)
	})
}

func connectorResult(at ConnectorAddress) *progressv1.OperationEvent {
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
