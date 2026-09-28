package aws_test

import (
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

var (
	defaultNamespace = bootstrap.Namespace(provider.DefaultNamespace)

	coreStackName, _    = defaultNamespace.StackNameFor(bootstrap.TierProduction)
	previewStackName, _ = defaultNamespace.StackNameFor(bootstrap.TierPreview)

	passphraseParam = defaultNamespace.PassphraseParamName()
)
