package control

import "github.com/ocelhq/ocel/platform/aws/provider/bootstrap"

var (
	coreStackName, _ = bootstrap.DefaultNamespace.StackNameFor(bootstrap.ClassProduction)
	edgeUserName, _  = bootstrap.DefaultNamespace.EdgeUserNameFor(bootstrap.ClassProduction)

	passphraseParam = bootstrap.DefaultNamespace.PassphraseParamName()
)
