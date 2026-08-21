package server

import (
	"context"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestGetCredentialPolicy(t *testing.T) {
	t.Parallel()

	document := func(t *testing.T, tier contractv1.CredentialTier) string {
		t.Helper()
		resp, err := (&Server{}).GetCredentialPolicy(context.Background(), &contractv1.CredentialPolicyRequest{Tier: tier})
		if err != nil {
			t.Fatalf("GetCredentialPolicy(%v) error = %v", tier, err)
		}
		return resp.GetDocument()
	}

	t.Run("each tier renders a policy of its own", func(t *testing.T) {
		t.Parallel()
		bootstrapped := document(t, contractv1.CredentialTier_CREDENTIAL_TIER_BOOTSTRAP)
		deployed := document(t, contractv1.CredentialTier_CREDENTIAL_TIER_DEPLOY)
		for tier, doc := range map[string]string{"bootstrap": bootstrapped, "deploy": deployed} {
			if !strings.HasPrefix(doc, `{"Statement":`) {
				t.Errorf("the %s tier rendered %q, want a policy document", tier, doc)
			}
		}
		if bootstrapped == deployed {
			t.Error("the two tiers render the same document, so they are one credential")
		}
	})

	t.Run("naming no tier is refused as a bad request", func(t *testing.T) {
		t.Parallel()
		_, err := (&Server{}).GetCredentialPolicy(context.Background(), &contractv1.CredentialPolicyRequest{})
		if connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Fatalf("GetCredentialPolicy(unspecified) error = %v, want an invalid-argument", err)
		}
	})
}
