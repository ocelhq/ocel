package bootstrap

import (
	"github.com/ocelhq/ocel/pkg/provider"
)

var (
	defaultNamespace = Namespace(provider.DefaultNamespace)

	coreStackName, _       = defaultNamespace.StackNameFor(TierProduction)
	previewStackName, _    = defaultNamespace.StackNameFor(TierPreview)
	edgeUserName, _        = defaultNamespace.EdgeUserNameFor(TierProduction)
	previewEdgeUser, _     = defaultNamespace.EdgeUserNameFor(TierPreview)
	originSecretParam, _   = defaultNamespace.OriginSecretParamFor(TierProduction)
	previewOriginSecret, _ = defaultNamespace.OriginSecretParamFor(TierPreview)

	passphraseParam = defaultNamespace.PassphraseParamName()
)
