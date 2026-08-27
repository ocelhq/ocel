package providerkit_test

import (
	"context"
	"slices"
	"testing"

	connect "connectrpc.com/connect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestPreflightReportsWhoThisRunIsAndWhatItCarries(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, provider := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureCache},
	})
	recordProject(t, provider, "blog")

	resp, err := client.Preflight(ctx, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}

	identity := resp.GetIdentity()
	if identity.GetProvider() != string(fake.Vendor) || identity.GetAccount() == "" || identity.GetPrincipal() == "" {
		t.Errorf("Preflight() identity = %+v, want the vendor, account and principal", identity)
	}
	details := identity.GetDetails()
	if len(details) != 1 || details[0].GetLabel() != "region" || details[0].GetValue() != "nowhere" {
		t.Errorf("Preflight() identity details = %+v, want the vendor's own wording", details)
	}
	if len(resp.GetCredentialProblems()) != 0 {
		t.Errorf("Preflight() reported %v, want credentials that answered to be no problem", resp.GetCredentialProblems())
	}

	if !resp.GetInfrastructurePresent() || resp.GetInfraTier() != environmentv1.Tier_TIER_PRODUCTION {
		t.Errorf("Preflight() = %v/%v, want the production bootstrap it just stood up", resp.GetInfrastructurePresent(), resp.GetInfraTier())
	}
	if !resp.GetBootstrap().GetPresent() || resp.GetBootstrap().GetWriter() != "1.2.3" {
		t.Errorf("Preflight() bootstrap = %+v, want it present and written by this build", resp.GetBootstrap())
	}
	if !slices.Equal(resp.GetKnownSlugs(), []string{"blog"}) {
		t.Errorf("Preflight() known slugs = %v, want the projects besides the one asking", resp.GetKnownSlugs())
	}
}

func TestPreflightReportsCredentialsThatWereDenied(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.2.3")
	provider.Credentials().(*fake.Credentials).Deny("run `fake login` and try again")

	resp, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v, want a credential problem rather than a failed call", err)
	}
	problems := resp.GetCredentialProblems()
	if len(problems) != 1 {
		t.Fatalf("Preflight() reported %d credential problems, want one", len(problems))
	}
	if problems[0].GetProvider() != string(fake.Vendor) {
		t.Errorf("credential problem names %q, want the vendor whose credentials were refused", problems[0].GetProvider())
	}
	if problems[0].GetHint() != "run `fake login` and try again" {
		t.Errorf("credential problem hint = %q, want the vendor's own wording", problems[0].GetHint())
	}
	if resp.GetBootstrap() != nil {
		t.Error("Preflight() described a bootstrap it could not authenticate to read")
	}
}

func TestPreflightRequiresTheFeaturesTheEdgeNeeds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, _ := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureCache},
	})

	resp, err := client.Preflight(ctx, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Edge:         &contractv1.EdgeSelection{Kind: "relay"},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	for _, stack := range resp.GetBootstrap().GetStacks() {
		if stack.GetFeature() == fake.FeatureCache && !stack.GetRequired() {
			t.Errorf("%s is not marked required, and the relay edge needs it", fake.FeatureCache)
		}
	}
}

func TestPreflightCarriesTheGlobalPreviewWildcard(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, _ := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PREVIEW})

	resp, err := client.Preflight(ctx, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PREVIEW})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if resp.GetPreviewWildcard() != nil {
		t.Errorf("Preflight() = %+v, want no wildcard before one is used", resp.GetPreviewWildcard())
	}

	if result := usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q, want the wildcard raised", result.GetError())
	}

	resp, err = client.Preflight(ctx, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PREVIEW})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	held := resp.GetPreviewWildcard()
	if held.GetBaseDomain() != "preview.acme.com" {
		t.Fatalf("Preflight() wildcard = %+v, want the recorded base domain", held)
	}
	if !held.GetRouteInstalled() {
		t.Error("Preflight() says the shared entry route is not installed, though the edge owns it")
	}
	if held.GetGrammarMin() != edge.PreviewGrammarMin || held.GetGrammarMax() != edge.PreviewGrammarMax {
		t.Errorf("Preflight() wildcard grammar = %d–%d, want %d–%d", held.GetGrammarMin(), held.GetGrammarMax(), edge.PreviewGrammarMin, edge.PreviewGrammarMax)
	}
}

func TestPreflightFallsBackToTheSiblingClass(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, _ := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})

	resp, err := client.Preflight(ctx, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PREVIEW})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if !resp.GetInfrastructurePresent() || resp.GetInfraTier() != environmentv1.Tier_TIER_PRODUCTION {
		t.Errorf("Preflight() = %v/%v, want it to name the production bootstrap that does stand", resp.GetInfrastructurePresent(), resp.GetInfraTier())
	}
	if resp.GetBootstrap().GetPresent() {
		t.Error("Preflight() reports a preview bootstrap that was never stood up")
	}
}

func TestPreflightRefusesABootstrapThisBuildCannotRead(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, provider := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	provider.Bootstrapper().AtSchema(providerkit.BootstrapSchema + 1)

	_, err := client.Preflight(ctx, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PRODUCTION})
	if got := connect.CodeOf(err); got != connect.CodeFailedPrecondition {
		t.Fatalf("Preflight() over a newer bootstrap: code = %v, want %v (%v)", got, connect.CodeFailedPrecondition, err)
	}
}
