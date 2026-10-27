package account

import (
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

var CredentialPolicies func(Namespace, edge.CredentialPurpose, environment.Tier, string) ([]iam.ManagedPolicy, error)

func DeployCredentialPermissions(ns Namespace, tier environment.Tier, broughtKey string) (string, error) {
	return renderedCredential(ns, edge.PurposeDeploy, tier, broughtKey)
}

func BootstrapCredentialPermissions(ns Namespace, tier environment.Tier, broughtKey string) (string, error) {
	return renderedCredential(ns, edge.PurposeBootstrap, tier, broughtKey)
}

func renderedCredential(ns Namespace, purpose edge.CredentialPurpose, tier environment.Tier, broughtKey string) (string, error) {
	policies, err := CredentialPolicies(ns, purpose, tier, broughtKey)
	if err != nil {
		return "", err
	}
	return iam.RenderManagedPolicies(string(purpose), policies)
}
