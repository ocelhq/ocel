package apigateway

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

func requireInvokeRole(ns bootstrap.Namespace, deployed bootstrap.Deployed, tier environment.Tier) (string, error) {
	arn := deployed.Outputs[bootstrap.OutputEdgeInvokeRoleARN]
	if arn == "" {
		return "", fmt.Errorf("the %s role API Gateway invokes this project's functions through does not exist in this account, so the %q edge has nothing to front the deployment with. It is provisioned by the %s feature stack, which this account has none of, and this deploy will not create it: run `%s` with this edge selected and deploy again", ns.EdgeInvokeRoleName(tier), Kind, bootstrap.FeatureAPIGatewayEdge, provider.BootstrapCommand(tier))
	}
	return arn, nil
}
