package control

import (
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

var (
	defaultNamespace = bootstrap.Namespace(providerkit.DefaultNamespace)

	coreStackName, _ = defaultNamespace.StackNameFor(bootstrap.ClassProduction)
	edgeUserName, _  = defaultNamespace.EdgeUserNameFor(bootstrap.ClassProduction)

	passphraseParam = defaultNamespace.PassphraseParamName()
)
