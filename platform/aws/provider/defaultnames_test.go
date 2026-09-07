package provider_test

import "github.com/ocelhq/ocel/platform/aws/provider/bootstrap"

var (
	coreStackName, _    = bootstrap.DefaultNamespace.StackNameFor(bootstrap.ClassProduction)
	previewStackName, _ = bootstrap.DefaultNamespace.StackNameFor(bootstrap.ClassPreview)

	passphraseParam = bootstrap.DefaultNamespace.PassphraseParamName()
)
