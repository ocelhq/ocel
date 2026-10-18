package apigateway

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
)

func requireInvokeRole(ns account.Namespace, deployed account.Deployed, tier environment.Tier) (string, error) {
	arn := deployed.Outputs[account.OutputEdgeInvokeRoleARN]
	if arn == "" {
		return "", fmt.Errorf("the %s role API Gateway invokes this project's functions through does not exist in this account, so the %q edge has nothing to front the deployment with. It is made with the %s bootstrap, which predates it: run `%s` and deploy again", ns.EdgeInvokeRoleName(tier), Kind, tier, provider.BootstrapCommand(tier))
	}
	return arn, nil
}
