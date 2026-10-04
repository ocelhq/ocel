package readiness

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/prerequisite"
	"github.com/ocelhq/ocel/cli/internal/project"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"google.golang.org/protobuf/encoding/protojson"
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

func infrastructureIn(tier environmentv1.Tier) *contractv1.PreflightResponse {
	return &contractv1.PreflightResponse{InfrastructurePresent: true, InfraTier: tier, Bootstrap: &contractv1.BootstrapStatus{Tier: tier, Present: true}}
}

func refusedFor(t *testing.T, resp *contractv1.PreflightResponse, cfg *project.Project, req Request) prerequisite.MissingError {
	t.Helper()
	err := RefuseUnready(resp, nil, cfg, req)
	var missing prerequisite.MissingError
	if !errors.As(err, &missing) {
		t.Fatalf("RefuseUnready err = %v, want a missing prerequisite", err)
	}
	return missing
}

func TestAMatchingInfrastructureTierIsNotRefused(t *testing.T) {
	for _, tier := range []environmentv1.Tier{environmentv1.Tier_TIER_PREVIEW, environmentv1.Tier_TIER_PRODUCTION} {
		if err := RefuseUnready(infrastructureIn(tier), nil, &project.Project{}, Request{Tier: tier, Require: Infrastructure}); err != nil {
			t.Errorf("RefuseUnready(%v) = %v, want nil", tier, err)
		}
	}
}

func TestATierWithNoInfrastructureIsABootstrapToSetUpNamingItsCommand(t *testing.T) {
	status := &contractv1.BootstrapStatus{Tier: environmentv1.Tier_TIER_PRODUCTION, Stacks: []*contractv1.BootstrapStack{
		{Name: "ocel-bootstrap", Required: true},
		{Name: "ocel-bootstrap-isr", Feature: "isr", Required: true},
		{Name: "ocel-bootstrap-queues", Feature: "queues"},
	}}
	resp := &contractv1.PreflightResponse{Bootstrap: status, Identity: &contractv1.Identity{Account: "203.0.113.7"}}
	missing := refusedFor(t, resp, &project.Project{}, Request{Tier: environmentv1.Tier_TIER_PRODUCTION, Require: Infrastructure})
	if missing.Missing() != prerequisite.Bootstrap {
		t.Errorf("missing = %v, want the bootstrap", missing.Missing())
	}
	if want := "Run `ocel bootstrap production --features isr` and try again"; !strings.HasSuffix(missing.Error(), want) {
		t.Errorf("error = %q, want it to end %q: the first bootstrap includes what the project needs", missing.Error(), want)
	}
	if !strings.Contains(missing.Finding(), "203.0.113.7") {
		t.Errorf("finding = %q, want it to name the account with nothing set up", missing.Finding())
	}
	var absent NoInfrastructureError
	if !errors.As(missing, &absent) {
		t.Fatalf("missing = %T, want a NoInfrastructureError", missing)
	}
	if req := absent.BootstrapRequest(); req.GetTier() != environmentv1.Tier_TIER_PRODUCTION || strings.Join(req.GetFeatures(), ",") != "isr" {
		t.Errorf("bootstrap request = %v, want production with isr", req)
	}
}

func TestAnotherTiersInfrastructureIsABootstrapToSetUpForThisOne(t *testing.T) {
	for _, c := range []struct {
		infra, required environmentv1.Tier
		names           string
	}{
		{environmentv1.Tier_TIER_PRODUCTION, environmentv1.Tier_TIER_PREVIEW, "preview"},
		{environmentv1.Tier_TIER_PREVIEW, environmentv1.Tier_TIER_PRODUCTION, "production"},
	} {
		missing := refusedFor(t, infrastructureIn(c.infra), &project.Project{}, Request{Tier: c.required, Require: Infrastructure})
		msg := missing.Error()
		if !strings.Contains(msg, c.names+" infrastructure") || !strings.HasSuffix(msg, "Run `ocel bootstrap "+c.names+"` and try again") {
			t.Errorf("error = %q, want it to name %s infrastructure and the bootstrap that sets it up", msg, c.names)
		}
		if strings.Contains(msg, "ocel deploy can only") || strings.Contains(msg, "ocel preview can only") {
			t.Errorf("error names a command the caller may not have run, got %q", msg)
		}
	}
}

func TestACommandNamingNoTierIsRefusedAnAccountWithNone(t *testing.T) {
	err := RefuseUnready(infrastructureIn(environmentv1.Tier_TIER_PREVIEW), nil, &project.Project{}, Request{Require: Infrastructure})
	if err == nil || !strings.Contains(err.Error(), "preview infrastructure") {
		t.Errorf("RefuseUnready err = %v, want it to name the infrastructure the account points at", err)
	}
}

func TestABootstrapLackingAFeatureTheProjectNeedsIsABootstrapToSetUp(t *testing.T) {
	resp := infrastructureIn(environmentv1.Tier_TIER_PRODUCTION)
	resp.Bootstrap = bootstrapOf(
		&contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, DigestCurrent: true, Required: true},
		&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Required: true},
	)
	missing := refusedFor(t, resp, &project.Project{}, Request{Tier: environmentv1.Tier_TIER_PRODUCTION, Require: Features})
	if missing.Missing() != prerequisite.Bootstrap || missing.Remedy() != "`ocel bootstrap production --features isr`" {
		t.Errorf("missing = %v %q, want the bootstrap that adds isr", missing.Missing(), missing.Remedy())
	}
	var lacking MissingFeaturesError
	if !errors.As(missing, &lacking) || strings.Join(lacking.BootstrapRequest().GetFeatures(), ",") != "isr" {
		t.Errorf("missing = %#v, want a MissingFeaturesError asking for isr", missing)
	}
}

func TestAProjectWithNoHostnameWhereTheRouterNeedsOneIsADomainToSetUp(t *testing.T) {
	resp := infrastructureIn(environmentv1.Tier_TIER_PRODUCTION)
	resp.HostnameRequired = true
	cfg := &project.Project{Slug: "shop", Path: "/code/shop/ocel.json"}
	req := Request{Tier: environmentv1.Tier_TIER_PRODUCTION, Require: Infrastructure, RequireHostname: true}
	missing := refusedFor(t, resp, cfg, req)
	if missing.Missing() != prerequisite.Domain {
		t.Errorf("missing = %v, want the domain", missing.Missing())
	}
	for _, want := range []string{"shop", "ocel.json", "domains"} {
		if !strings.Contains(missing.Error(), want) {
			t.Errorf("error = %q, want it to name %q", missing.Error(), want)
		}
	}
	var unnamed NoHostnameError
	if !errors.As(missing, &unnamed) || unnamed.ConfigPath != cfg.Path || unnamed.Slug != "shop" {
		t.Errorf("missing = %#v, want the config to add the hostname to", missing)
	}

	t.Run("a declared hostname is enough", func(t *testing.T) {
		declared := &project.Project{Slug: "shop", Domains: project.Domains{Production: []string{"shop.example.com"}}}
		if err := RefuseUnready(resp, nil, declared, req); err != nil {
			t.Errorf("RefuseUnready err = %v, want a project with a hostname let through", err)
		}
	})
	t.Run("a router that addresses itself needs none", func(t *testing.T) {
		addressed := infrastructureIn(environmentv1.Tier_TIER_PRODUCTION)
		if err := RefuseUnready(addressed, nil, cfg, req); err != nil {
			t.Errorf("RefuseUnready err = %v, want no hostname asked for", err)
		}
	})
	t.Run("a command that serves nothing asks for none", func(t *testing.T) {
		if err := RefuseUnready(resp, nil, cfg, Request{Tier: environmentv1.Tier_TIER_PRODUCTION, Require: Infrastructure}); err != nil {
			t.Errorf("RefuseUnready err = %v, want no hostname asked for", err)
		}
	})
}

func TestATierWithNoInfrastructureReportsBootstrapMissingHintingTheCommandThroughWrapping(t *testing.T) {
	resp := &contractv1.PreflightResponse{Bootstrap: &contractv1.BootstrapStatus{Tier: environmentv1.Tier_TIER_PRODUCTION, Stacks: []*contractv1.BootstrapStack{
		{Name: "ocel-bootstrap-isr", Feature: "isr", Required: true},
	}}}
	err := RefuseUnready(resp, nil, &project.Project{}, Request{Tier: environmentv1.Tier_TIER_PRODUCTION, Require: Infrastructure})

	got := clierror.NewRunError(fmt.Errorf("deploy: %w", fmt.Errorf("checking: %w", err)))
	if got.GetCode() != "bootstrap.missing" || got.GetHint() != "ocel bootstrap production --features isr" {
		t.Fatalf("run error = %s, want bootstrap.missing hinting the bootstrap command", protojson.Format(got))
	}
	var missing prerequisite.MissingError
	var absent NoInfrastructureError
	if !errors.As(err, &missing) || !errors.As(err, &absent) {
		t.Errorf("err = %T, want the prerequisite and its typed error still found through the code", err)
	}
	if !strings.HasPrefix(err.Error(), "no production infrastructure is set up yet.") {
		t.Errorf("error = %q, want the unchanged human text", err.Error())
	}
}

func TestAnotherTiersInfrastructureReportsBootstrapMissingHintingTheTierToSetUp(t *testing.T) {
	err := RefuseUnready(infrastructureIn(environmentv1.Tier_TIER_PRODUCTION), nil, &project.Project{}, Request{Tier: environmentv1.Tier_TIER_PREVIEW, Require: Infrastructure})

	got := clierror.NewRunError(err)
	if got.GetCode() != "bootstrap.missing" || got.GetHint() != "ocel bootstrap preview" {
		t.Fatalf("run error = %s, want bootstrap.missing hinting ocel bootstrap preview", protojson.Format(got))
	}
}

func TestABootstrapLackingAFeatureReportsBootstrapFeaturesMissingHintingTheRepair(t *testing.T) {
	resp := infrastructureIn(environmentv1.Tier_TIER_PRODUCTION)
	resp.Bootstrap = bootstrapOf(
		&contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, DigestCurrent: true, Required: true},
		&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Required: true},
	)
	err := RefuseUnready(resp, nil, &project.Project{}, Request{Tier: environmentv1.Tier_TIER_PRODUCTION, Require: Features})

	got := clierror.NewRunError(fmt.Errorf("deploy: %w", err))
	if got.GetCode() != "bootstrap.features_missing" || got.GetHint() != "ocel bootstrap production --features isr" {
		t.Fatalf("run error = %s, want bootstrap.features_missing hinting the repair command", protojson.Format(got))
	}
	var lacking MissingFeaturesError
	if !errors.As(err, &lacking) {
		t.Errorf("err = %T, want the typed error still found through the code", err)
	}
	if !strings.HasPrefix(err.Error(), "the production bootstrap does not include what this project needs: isr.") {
		t.Errorf("error = %q, want the unchanged human text", err.Error())
	}
}

func TestAGapRefusingMissingFeaturesReportsBootstrapFeaturesMissing(t *testing.T) {
	gap := Gap{Missing: []string{"isr"}, Features: []string{"isr"}}
	for name, err := range map[string]error{
		"RefuseMissing":    gap.RefuseMissing(environmentv1.Tier_TIER_PRODUCTION),
		"RefuseIncomplete": gap.RefuseIncomplete(environmentv1.Tier_TIER_PRODUCTION),
	} {
		if got := clierror.NewRunError(err); got.GetCode() != "bootstrap.features_missing" {
			t.Errorf("%s: run error = %s, want bootstrap.features_missing", name, protojson.Format(got))
		}
	}
}
