package preflight

import (
	"strings"
	"testing"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestIdentityEvent(t *testing.T) {
	t.Parallel()

	cfg := &projectconfig.Config{Slug: "acme", Edge: &projectconfig.EdgeDescriptor{Kind: "cloudflare"}}

	t.Run("names the project, the tier and both parties", func(t *testing.T) {
		t.Parallel()

		got := IdentityEvent(cfg, environmentv1.Tier_TIER_PRODUCTION, &contractv1.Identity{
			Provider:  "AWS",
			Account:   "123456789012",
			Principal: "deploy",
			Location:  "us-east-1",
			EdgeScope: "a1b2c3d4",
			Details:   []*contractv1.Detail{{Label: "profile", Value: "default"}},
		})
		want := &streamv1.IdentityEvent{
			Project: "acme",
			Tier:    environmentv1.Tier_TIER_PRODUCTION,
			Origin:  &streamv1.Party{Vendor: "aws", Account: "123456789012", Principal: "deploy", Location: "us-east-1"},
			Edge:    &streamv1.Party{Vendor: "cloudflare", Account: "a1b2c3d4"},
		}
		if !proto.Equal(got, want) {
			t.Errorf("IdentityEvent() = %v, want %v", got, want)
		}
	})

	t.Run("no edge scope means no edge party", func(t *testing.T) {
		t.Parallel()

		got := IdentityEvent(cfg, environmentv1.Tier_TIER_PREVIEW, &contractv1.Identity{
			Provider: "vps",
			Account:  "srv1.example.com",
		})
		if got.GetEdge() != nil {
			t.Errorf("edge = %v, want nothing: the provider reported no edge scope", got.GetEdge())
		}
	})

	t.Run("an empty identity carries no origin", func(t *testing.T) {
		t.Parallel()

		if got := IdentityEvent(cfg, environmentv1.Tier_TIER_PREVIEW, &contractv1.Identity{}); got.GetOrigin() != nil {
			t.Errorf("origin = %v, want nothing to stand for an identity the provider left blank", got.GetOrigin())
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
