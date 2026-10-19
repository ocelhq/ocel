package apigateway

import (
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
)

func ListProjectResources(state edge.StackState, region string) ([]provider.PhysicalResource, error) {
	var own private
	if err := state.Private.Into(&own); err != nil || own.API == "" {
		return nil, err
	}
	return []provider.PhysicalResource{cfn.NewPhysicalResource("AWS::ApiGateway::RestApi", own.API, region)}, nil
}
