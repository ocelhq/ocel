package control

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/account"
)

var (
	defaultNamespace = account.Namespace(provider.DefaultNamespace)

	coreStackName, _ = defaultNamespace.StackNameFor(environment.TierProduction)
	edgeUserName, _  = defaultNamespace.EdgeUserNameFor(environment.TierProduction)

	passphraseParam = defaultNamespace.PassphraseParamFor(environment.TierProduction)
)
