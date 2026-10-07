package aws

import (
	"context"

	"github.com/ocelhq/ocel/pkg/kvstore"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
)

func (p *Provider) ForwardPorts(ctx context.Context, req provider.PortForwardRequest) ([]provider.PortForward, error) {
	if len(req.Bindings) == 0 {
		return nil, nil
	}
	deployed, err := p.bootstrapped(ctx, req.Tier)
	if err != nil {
		return nil, err
	}
	if err := p.requireBootstrapped(deployed, req.Tier); err != nil {
		return nil, err
	}
	clients := bastion.NewClients(p.aws)
	reconciled, err := bastion.Reconcile(ctx, clients, bastion.Spec{
		Tier:     req.Tier,
		Boundary: deployed.AppBoundaryARN,
		Ports:    []int{deploy.PostgresPort, kvstore.ValkeyPort},
	})
	if err != nil {
		return nil, err
	}
	return reconciled.Forward(ctx, clients, bastion.OpenSession(p.aws), req.ReportFailure, req.Bindings)
}
