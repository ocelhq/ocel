package cloudfront

import (
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

var (
	defaultNamespace = bootstrap.Namespace(providerkit.DefaultNamespace)

	coreStackName, _ = defaultNamespace.StackNameFor(bootstrap.ClassProduction)
)
