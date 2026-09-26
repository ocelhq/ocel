package aws_test

import (
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

var (
	defaultNamespace = bootstrap.Namespace(provider.DefaultNamespace)

	coreStackName, _    = defaultNamespace.StackNameFor(bootstrap.ClassProduction)
	previewStackName, _ = defaultNamespace.StackNameFor(bootstrap.ClassPreview)

	passphraseParam = defaultNamespace.PassphraseParamName()
)
