package control

import (
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

var (
	defaultNamespace = bootstrap.Namespace(provider.DefaultNamespace)

	coreStackName, _ = defaultNamespace.StackNameFor(bootstrap.TierProduction)
	edgeUserName, _  = defaultNamespace.EdgeUserNameFor(bootstrap.TierProduction)

	passphraseParam = defaultNamespace.PassphraseParamName()
)
