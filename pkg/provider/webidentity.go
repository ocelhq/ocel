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

const ConsoleTrustPart = "console-trust"

type WebIdentityTrust struct {
	Issuer         string
	Audience       string
	OrganizationID string
	TargetID       string
}

type TierIdentity struct {
	IdentityProvider string
	Principal        string
}
