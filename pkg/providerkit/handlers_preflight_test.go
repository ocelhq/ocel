package providerkit_test

import (
	"context"
	"errors"
	"slices"
	"strings"
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

func TestPreflightNamesWhoAlreadyServesEachHostnameThisProjectDeclares(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Owns("acme.com", "ocel-other-production")

	resp, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
		Domains:      []string{"acme.com", "free.example.com"},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}

	claims := resp.GetDomainClaims()
	if len(claims) != 2 {
		t.Fatalf("Preflight() reported %d claims for two hostnames: %+v", len(claims), claims)
	}
	if claims[0].GetHostname() != "acme.com" || claims[0].GetStatus() != contractv1.DomainClaim_STATUS_CLAIMED {
		t.Errorf("claim for the served hostname = %+v, want it claimed", claims[0])
	}
	if claims[0].GetOwner() != "ocel-other-production" {
		t.Errorf("claim owner = %q, want the surface the edge says serves it", claims[0].GetOwner())
	}
	if claims[1].GetHostname() != "free.example.com" || claims[1].GetStatus() != contractv1.DomainClaim_STATUS_UNCLAIMED {
		t.Errorf("claim for the hostname nothing serves = %+v, want it unclaimed and owned by nobody", claims[1])
	}
}

func TestPreflightDoesNotReportThisProjectsOwnHostnameAsSomeoneElsesClaim(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	seedStack(t, provider, providerkit.ClassProduction, "shop", providerkit.EdgeStackState{
		Edge: edge.StackState{Slug: "shop", Class: providerkit.ClassProduction, Bound: []string{"acme.com"}},
	})
	provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Owns("acme.com", "ocel-shop-production")

	resp, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
		Domains:      []string{"acme.com"},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	claims := resp.GetDomainClaims()
	if len(claims) != 1 || claims[0].GetStatus() != contractv1.DomainClaim_STATUS_UNCLAIMED {
		t.Fatalf("Preflight() reported %+v for a hostname this project already serves, want it unclaimed: a redeploy would otherwise be refused for holding its own domain", claims)
	}
}

func TestPreflightDoesNotRefuseAHostnameThisProjectAlreadyClaimsButNeverRecorded(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	seedStack(t, provider, providerkit.ClassProduction, "shop", providerkit.EdgeStackState{
		Edge: edge.StackState{Slug: "shop", Class: providerkit.ClassProduction},
	})
	provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Owns("acme.com", "ocel-shop-production")

	resp, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
		Domains:      []string{"acme.com"},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	claims := resp.GetDomainClaims()
	if len(claims) != 1 || claims[0].GetStatus() != contractv1.DomainClaim_STATUS_UNCLAIMED {
		t.Fatalf("Preflight() reported %+v for a hostname this project's own surface serves on the edge while its record says nothing is bound, want it unclaimed: a bind that wrote the route and stopped before the record would otherwise lock the project out of its own hostname, and the refusal would tell it to tear itself down", claims)
	}
}

func TestPreflightReportsAnUnreadableOwnerRatherThanStoppingTheDeploy(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	provider.Edges().(*fake.Edges).Edge(fake.KindRelay).OwnersUnreadable(errors.New("the edge was throttled listing what it serves"))

	resp, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
		Domains:      []string{"acme.com"},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v, want a deploy that carries on: who serves a hostname is advisory, and a provider that hiccups enumerating owners has said nothing about this project", err)
	}
	claims := resp.GetDomainClaims()
	if len(claims) != 1 || claims[0].GetStatus() != contractv1.DomainClaim_STATUS_UNSPECIFIED {
		t.Fatalf("Preflight() reported %+v for a hostname whose owner could not be read, want it unanswered rather than claimed or cleared", claims)
	}
	if !strings.Contains(claims[0].GetCause(), "throttled") {
		t.Errorf("claim cause = %q, want the reason the owner could not be read: a guard that goes quiet without saying why is one nobody can tell from a hostname nobody holds", claims[0].GetCause())
	}
}

func TestPreflightTreatsTheSharedPreviewEntryAsNobodysClaim(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PREVIEW})
	wildcard := edge.PreviewWildcard("previews.example.com")
	provider.Edges().(*fake.Edges).Edge(fake.KindRelay).Owns(wildcard, edge.PreviewEntryOwner)

	resp, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PREVIEW,
		Slug:         "shop",
		Domains:      []string{wildcard},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	claims := resp.GetDomainClaims()
	if len(claims) != 1 || claims[0].GetStatus() != contractv1.DomainClaim_STATUS_UNCLAIMED {
		t.Fatalf("Preflight() reported %+v for the wildcard every project's previews share, want it unclaimed", claims)
	}
}
