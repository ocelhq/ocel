package aws

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/service/ec2"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/iam"

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
	clients := bastion.Clients{ECS: ecs.NewFromConfig(p.aws), IAM: iam.NewFromConfig(p.aws), EC2: ec2.NewFromConfig(p.aws)}
	ensured, err := bastion.Ensure(ctx, clients, bastion.Spec{
		Tier:     req.Tier,
		Boundary: deployed.AppBoundaryARN,
		Ports:    []int{deploy.PostgresPort, kvstore.ValkeyPort},
	})
	if err != nil {
		return nil, err
	}
	return ensured.ForwardBindings(ctx, clients, bastion.OpenSession(p.aws), reportPortForwardProblem, req.Bindings)
}

func reportPortForwardProblem(err error) {
	fmt.Fprintf(os.Stderr, "ocel: port forward: %v\n", err)
}
