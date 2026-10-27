package provider

type WebIdentity struct {
	TokenFile        string
	IdentityProvider string
	Principal        string
	SessionName      string
}

type WebIdentityFacts struct {
	CredentialEnvVars []string
}
