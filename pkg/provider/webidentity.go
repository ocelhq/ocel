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

func (f WebIdentityFacts) FindOwnCredentialEnvVars(getenv func(string) string) []string {
	var set []string
	for _, name := range f.CredentialEnvVars {
		if getenv(name) != "" {
			set = append(set, name)
		}
	}
	return set
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
