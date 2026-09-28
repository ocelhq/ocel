package aws_test

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

var (
	defaultNamespace = bootstrap.Namespace(provider.DefaultNamespace)

	coreStackName, _    = defaultNamespace.StackNameFor(environment.TierProduction)
	previewStackName, _ = defaultNamespace.StackNameFor(environment.TierPreview)

	passphraseParam = defaultNamespace.PassphraseParamName()
)
