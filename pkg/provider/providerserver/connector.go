package providerserver

import (
	"context"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
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
		Os:                described.OS,
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
		unit := UnitSpan(naming.UnitConnector, naming.UnitConnector, connectorInstallTitle(req), progressv1.Phase_PHASE_PROVISION)
		err := inSpan(sender, unit, func(_ *eventStream, progress progress.Log) error {
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
	unit := UnitSpan(naming.UnitConnector, naming.UnitConnector, progress.Removing.Title("the connector from this account"), progressv1.Phase_PHASE_DESTROY)
	return streamed(ctx, stream, unit, func(sender *eventStream, progress progress.Log) error {
		sender.refusing(connect.CodeUnimplemented)
		return connector.Remove(ctx, progress)
	})
}

func connectorInstallTitle(req *contractv1.InstallConnectorRequest) progress.Title {
	title := "the connector"
	if version := req.GetVersion(); version != "" {
		title += " " + version
	}
	if compute := req.GetCompute(); compute != "" {
		title += " on " + compute + " compute"
	}
	return progress.Installing.Title(title + " in this account")
}

func connectorResult(at provider.ConnectorAddress) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Body: &progressv1.OperationEvent_Result{Result: &progressv1.ResultEvent{
			Success: true,
			Connector: &progressv1.ConnectorInstalled{
				Url:       at.URL,
				PublicKey: at.PublicKey,
				Compute:   string(at.Compute),
			},
		}},
	}
}
