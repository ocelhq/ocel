package conformance

import (
	"context"
	"errors"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func RunCredentials(t *testing.T, facts provider.Facts, credentials provider.Credentials) {
	t.Helper()

	ctx := context.Background()

	t.Run("Whoami either says who this is or refuses as denied", func(t *testing.T) {
		principal, err := credentials.Whoami(ctx)
		if err != nil {
			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied {
				t.Fatalf("Whoami() failed with %v, want a Refusal with code %s so the CLI can render a credential problem", err, refusal.CodeDenied)
			}
			if refused.Message == "" {
				t.Error("Whoami() refused with no message, so the CLI has nothing to tell the user")
			}
			return
		}
		if principal.Vendor == "" {
			t.Error("Whoami() answered a principal naming no provider")
		}
		for _, detail := range principal.Details {
			if detail.Label == "" {
				t.Errorf("Whoami() returned a detail with no label: %+v", detail)
			}
		}
	})

	t.Run("permissions are rendered for either purpose in either tier, or said not to exist yet", func(t *testing.T) {
		for _, purpose := range []edge.CredentialPurpose{edge.PurposeBootstrap, edge.PurposeDeploy} {
			for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
				if err := permissionsRendered(credentials, purpose, tier); err != nil {
					t.Errorf("Permissions(%s, %s) = %v, want the permissions that purpose needs or a %s refusal saying there are none to render yet",
						purpose, tier, err, refusal.CodeNotReady)
				}
			}
		}
	})

	t.Run("a provider that deploys with a web identity names only environment variables as its explicit credentials", func(t *testing.T) {
		if facts.WebIdentity != nil && slices.Contains(facts.WebIdentity.CredentialEnvVars, "") {
			t.Errorf("WebIdentity.CredentialEnvVars = %q, want only names of environment variables", facts.WebIdentity.CredentialEnvVars)
		}
	})
}

func permissionsRendered(credentials provider.Credentials, purpose edge.CredentialPurpose, tier environment.Tier) error {
	_, err := credentials.Permissions(purpose, tier)
	var refused refusal.Refusal
	if errors.As(err, &refused) && refused.Code == refusal.CodeNotReady {
		return nil
	}
	return err
}
