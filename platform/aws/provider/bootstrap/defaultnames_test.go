package bootstrap

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
)

var (
	defaultNamespace = Namespace(provider.DefaultNamespace)

	coreStackName, _       = defaultNamespace.StackNameFor(environment.TierProduction)
	previewStackName, _    = defaultNamespace.StackNameFor(environment.TierPreview)
	edgeUserName, _        = defaultNamespace.EdgeUserNameFor(environment.TierProduction)
	previewEdgeUser, _     = defaultNamespace.EdgeUserNameFor(environment.TierPreview)
	originSecretParam, _   = defaultNamespace.OriginSecretParamFor(environment.TierProduction)
	previewOriginSecret, _ = defaultNamespace.OriginSecretParamFor(environment.TierPreview)

	passphraseParam = defaultNamespace.PassphraseParamName()
)
