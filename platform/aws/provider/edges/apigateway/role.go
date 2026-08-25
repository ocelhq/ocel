package apigateway

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const invokePolicyName = "ocel-edge-invoke"

func invokeRoleName(class edge.Class) string {
	if class == edge.ClassPreview {
		return "ocel-edge-invoke-preview"
	}
	return "ocel-edge-invoke"
}

func requireInvokeRole(deployed bootstrap.Deployed, class edge.Class) (string, error) {
	arn := deployed.CoreOutputs[OutputInvokeRoleARN]
	if arn == "" {
		return "", fmt.Errorf("the %s role API Gateway invokes this project's functions through does not exist in this account, so the %q edge has nothing to front the deployment with. It is created once per account when you bootstrap, and this deploy will not create it: run `%s` and deploy again", invokeRoleName(class), Kind, providerkit.BootstrapCommand(class))
	}
	return arn, nil
}
