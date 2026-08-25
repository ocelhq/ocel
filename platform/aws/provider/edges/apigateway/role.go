package apigateway

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func requireInvokeRole(deployed bootstrap.Deployed, class edge.Class) (string, error) {
	arn := deployed.Outputs[bootstrap.OutputEdgeInvokeRoleARN]
	if arn == "" {
		return "", fmt.Errorf("the %s role API Gateway invokes this project's functions through does not exist in this account, so the %q edge has nothing to front the deployment with. It stands in the %s feature stack, which this account has none of, and this deploy will not create it: run `%s` with this edge selected and deploy again", bootstrap.EdgeInvokeRoleName(class), Kind, bootstrap.FeatureAPIGatewayEdge, providerkit.BootstrapCommand(class))
	}
	return arn, nil
}
