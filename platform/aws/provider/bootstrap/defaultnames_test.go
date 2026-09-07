package bootstrap

var (
	coreStackName, _       = DefaultNamespace.StackNameFor(ClassProduction)
	previewStackName, _    = DefaultNamespace.StackNameFor(ClassPreview)
	edgeUserName, _        = DefaultNamespace.EdgeUserNameFor(ClassProduction)
	previewEdgeUser, _     = DefaultNamespace.EdgeUserNameFor(ClassPreview)
	originSecretParam, _   = DefaultNamespace.OriginSecretParamFor(ClassProduction)
	previewOriginSecret, _ = DefaultNamespace.OriginSecretParamFor(ClassPreview)

	passphraseParam = DefaultNamespace.PassphraseParamName()
)
