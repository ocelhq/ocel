package boxapi

import "github.com/ocelhq/ocel/pkg/environment"

const (
	AuthorityDir         = TierRoot + "/ssh"
	AuthorityKey         = AuthorityDir + "/user-ca"
	AuthorityPublicKey   = AuthorityKey + ".pub"
	AuthorityConfigPath  = AuthorityDir + "/authority.json"
	AuthorizedPrincipals = AuthorityDir + "/principals"
	HostKeysPattern      = "/etc/ssh/ssh_host_*_key.pub"
)

type AuthorityConfig struct {
	Principal string `json:"principal"`
}

func ConsoleTrustPath(tier environment.Tier) string { return TierDir(tier) + "/console-trust.json" }

func ConsoleTrustDigestsPath(tier environment.Tier) string {
	return TierDir(tier) + "/console-trust.digests.json"
}

type ConsoleTrust struct {
	Issuer         string `json:"issuer"`
	Audience       string `json:"audience"`
	OrganizationID string `json:"organizationId"`
	TargetID       string `json:"targetId"`
}

const SignCommand = "ssh-sign"

func SignArgv() []string { return []string{"sudo", "-n", LiveBinary, SignCommand} }

type CertificateRequest struct {
	PublicKey   string `json:"publicKey"`
	Principal   string `json:"principal"`
	KeyID       string `json:"keyId"`
	ValidAfter  int64  `json:"validAfter"`
	ValidBefore int64  `json:"validBefore"`
}
