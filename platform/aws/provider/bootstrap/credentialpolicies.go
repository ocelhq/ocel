package bootstrap

import (
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

func credentialPolicies(ns Namespace, purpose string, tier environment.Tier, grants []iam.Statement) (string, error) {
	policies, err := iam.Split(purpose, func(i int) string { return ns.PolicyName(fmt.Sprintf("%s-%s-%d", purpose, tier, i)) }, grants)
	if err != nil {
		return "", err
	}
	return iam.RenderManagedPolicies(purpose, policies)
}
