package bootstrap

import "github.com/ocelhq/ocel/pkg/providerkit"

var (
	defaultNamespace = Namespace(providerkit.DefaultNamespace)

	coreStackName, _       = defaultNamespace.StackNameFor(ClassProduction)
	previewStackName, _    = defaultNamespace.StackNameFor(ClassPreview)
	edgeUserName, _        = defaultNamespace.EdgeUserNameFor(ClassProduction)
	previewEdgeUser, _     = defaultNamespace.EdgeUserNameFor(ClassPreview)
	originSecretParam, _   = defaultNamespace.OriginSecretParamFor(ClassProduction)
	previewOriginSecret, _ = defaultNamespace.OriginSecretParamFor(ClassPreview)

	passphraseParam = defaultNamespace.PassphraseParamName()
)
