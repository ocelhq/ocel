package cli

import (
	"strings"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestFormatIdentityBanner(t *testing.T) {
	t.Parallel()

	t.Run("names the provider, profile, account and region alongside the edge", func(t *testing.T) {
		t.Parallel()

		got := formatIdentityBanner(&contractv1.Identity{
			Provider:  "AWS",
			Profile:   "default",
			Account:   "123456789012",
			Region:    "us-east-1",
			Principal: "deploy",
			EdgeScope: "abcd1234",
		})
		for _, want := range []string{"Running with:", "AWS", "profile=default", "account=123456789012", "region=us-east-1", "Edge", "abcd1234"} {
			if !strings.Contains(got, want) {
				t.Errorf("banner missing %q:\n%s", want, got)
			}
		}
	})

	t.Run("profile falls back to the principal", func(t *testing.T) {
		t.Parallel()

		got := formatIdentityBanner(&contractv1.Identity{
			Provider:  "AWS",
			Account:   "123456789012",
			Region:    "eu-west-1",
			Principal: "session",
		})
		if strings.Contains(got, "profile=") {
			t.Errorf("expected no profile= when the provider reports none:\n%s", got)
		}
		if !strings.Contains(got, "identity=session") {
			t.Errorf("expected identity to fall back to the principal:\n%s", got)
		}
	})

	t.Run("an empty identity is blank", func(t *testing.T) {
		t.Parallel()

		if got := formatIdentityBanner(&contractv1.Identity{}); got != "" {
			t.Errorf("expected blank banner for empty identity, got %q", got)
		}
		if got := formatIdentityBanner(nil); got != "" {
			t.Errorf("expected blank banner for nil identity, got %q", got)
		}
	})
}

func TestCredentialProblems(t *testing.T) {
	t.Parallel()

	t.Run("nil when there are none", func(t *testing.T) {
		t.Parallel()

		if err := credentialProblems(nil); err != nil {
			t.Errorf("expected nil error for no problems, got %v", err)
		}
	})

	t.Run("aggregates all of them", func(t *testing.T) {
		t.Parallel()

		err := credentialProblems([]*contractv1.CredentialProblem{
			{Provider: "AWS", Message: "could not authenticate", Hint: "run aws sso login"},
			{Provider: "Cloudflare", Message: "CLOUDFLARE_API_TOKEN is not set", Hint: "export it"},
		})
		if err == nil {
			t.Fatal("expected an error aggregating the problems")
		}
		for _, want := range []string{"AWS", "could not authenticate", "run aws sso login", "Cloudflare", "CLOUDFLARE_API_TOKEN is not set", "export it"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("aggregated error missing %q:\n%s", want, err.Error())
			}
		}
	})
}
