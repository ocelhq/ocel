package readiness

import (
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/project"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"google.golang.org/protobuf/proto"
)

func TestTheIdentityEventNamesTheProjectTheTierAndBothParties(t *testing.T) {
	t.Parallel()

	cfg := &project.Project{Slug: "acme", Edge: &project.Edge{Kind: "relay"}}

	t.Run("names the project, the tier and both parties", func(t *testing.T) {
		t.Parallel()

		got := identityEvent(cfg, environmentv1.Tier_TIER_PRODUCTION, &contractv1.Identity{
			Provider:  "fake",
			Account:   "123456789012",
			Principal: "deploy",
			Location:  "zone-a",
			EdgeScope: "a1b2c3d4",
			Details:   []*contractv1.Detail{{Label: "profile", Value: "default"}},
		})
		want := &streamv1.IdentityEvent{
			Project: "acme",
			Tier:    environmentv1.Tier_TIER_PRODUCTION,
			Origin:  &streamv1.Party{Vendor: "fake", Account: "123456789012", Principal: "deploy", Location: "zone-a"},
			Edge:    &streamv1.Party{Vendor: "relay", Account: "a1b2c3d4"},
		}
		if !proto.Equal(got, want) {
			t.Errorf("identityEvent() = %v, want %v", got, want)
		}
	})

	t.Run("no edge scope means no edge party", func(t *testing.T) {
		t.Parallel()

		got := identityEvent(cfg, environmentv1.Tier_TIER_PREVIEW, &contractv1.Identity{
			Provider: "fake",
			Account:  "srv1.example.com",
		})
		if got.GetEdge() != nil {
			t.Errorf("edge = %v, want nothing: the provider reported no edge scope", got.GetEdge())
		}
	})

	t.Run("an empty identity has no origin", func(t *testing.T) {
		t.Parallel()

		if got := identityEvent(cfg, environmentv1.Tier_TIER_PREVIEW, &contractv1.Identity{}); got.GetOrigin() != nil {
			t.Errorf("origin = %v, want nothing to represent an identity the provider left blank", got.GetOrigin())
		}
	})
}

func TestCredentialProblemsAreNilWhenThereAreNoneAndAggregatedOtherwise(t *testing.T) {
	t.Parallel()

	t.Run("nil when there are none", func(t *testing.T) {
		t.Parallel()

		if err := refuseCredentialProblems(nil); err != nil {
			t.Errorf("expected nil error for no problems, got %v", err)
		}
	})

	t.Run("aggregates all of them", func(t *testing.T) {
		t.Parallel()

		err := refuseCredentialProblems([]*contractv1.CredentialProblem{
			{Provider: "Fake", Message: "could not authenticate", Hint: "run fake login"},
			{Provider: "Relay", Message: "FAKE_RELAY_TOKEN is not set", Hint: "export it"},
		})
		if err == nil {
			t.Fatal("expected an error aggregating the problems")
		}
		for _, want := range []string{"Fake", "could not authenticate", "run fake login", "Relay", "FAKE_RELAY_TOKEN is not set", "export it"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("aggregated error missing %q:\n%s", want, err.Error())
			}
		}
	})
}

func TestAMatchingInfrastructureTierIsNotRefused(t *testing.T) {
	cases := []struct {
		infra, required environmentv1.Tier
	}{
		{environmentv1.Tier_TIER_PREVIEW, environmentv1.Tier_TIER_PREVIEW},
		{environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PRODUCTION},
	}
	for _, c := range cases {
		if err := refuseOtherTier(c.infra, c.required); err != nil {
			t.Errorf("refuseOtherTier(%v, %v) = %v, want nil", c.infra, c.required, err)
		}
	}
}

func TestAMismatchedInfrastructureTierIsRefused(t *testing.T) {
	cases := []struct {
		infra, required environmentv1.Tier
	}{
		{environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW},
		{environmentv1.Tier_TIER_PREVIEW, environmentv1.Tier_TIER_PRODUCTION},
		{environmentv1.Tier_TIER_UNSPECIFIED, environmentv1.Tier_TIER_PREVIEW},
		{environmentv1.Tier_TIER_UNSPECIFIED, environmentv1.Tier_TIER_PRODUCTION},
	}
	for _, c := range cases {
		err := refuseOtherTier(c.infra, c.required)
		if err == nil {
			t.Errorf("refuseOtherTier(%v, %v) = nil, want error", c.infra, c.required)
			continue
		}
		if !strings.Contains(err.Error(), "infrastructure") {
			t.Errorf("refuseOtherTier(%v, %v) error names no infrastructure: %q", c.infra, c.required, err)
		}
	}
}

func TestAnotherTiersRefusalNamesTheInfrastructureAndTheBootstrapCommand(t *testing.T) {
	err := refuseOtherTier(environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW)
	if err == nil {
		t.Fatal("expected error")
	}
	msg := err.Error()
	if !strings.Contains(msg, "preview infrastructure") {
		t.Errorf("error should name preview infrastructure, got %q", msg)
	}
	if !strings.Contains(msg, "ocel bootstrap preview") {
		t.Errorf("error should tell the user how to fix it, got %q", msg)
	}
	if strings.Contains(msg, "ocel preview can only") {
		t.Errorf("error names a command the caller may not have run, got %q", msg)
	}

	err = refuseOtherTier(environmentv1.Tier_TIER_PREVIEW, environmentv1.Tier_TIER_PRODUCTION)
	if err == nil {
		t.Fatal("expected error")
	}
	msg = err.Error()
	if !strings.Contains(msg, "production infrastructure") {
		t.Errorf("error should name production infrastructure, got %q", msg)
	}
	if !strings.Contains(msg, "ocel bootstrap production") {
		t.Errorf("error should tell the user how to fix it, got %q", msg)
	}
	if strings.Contains(msg, "ocel deploy can only") {
		t.Errorf("error names a command the caller may not have run, got %q", msg)
	}
}
