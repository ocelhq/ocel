package apigateway

import "github.com/ocelhq/ocel/platform/aws/provider/bootstrap"

var (
	coreStackName, _ = bootstrap.DefaultNamespace.StackNameFor(bootstrap.ClassProduction)
)
