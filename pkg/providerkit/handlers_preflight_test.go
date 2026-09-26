package providerkit_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/arch"
	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
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

func TestPreflightNamesTheArchitectureContainerImagesAreBuiltFor(t *testing.T) {
	t.Parallel()

	provider := fake.NewProvider(fake.Options{Region: "nowhere"})
	client := servedBy(t, provider.WrappingContainers("arm64", []byte("runtime")))

	resp, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
		Containers: []*contractv1.ContainerApp{
			{App: "web"},
			{App: "worker", Arch: arch.X8664},
		},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if want := map[string]string{"web": "arm64", "worker": "amd64"}; !maps.Equal(resp.GetContainerArchs(), want) {
		t.Errorf("Preflight() container archs = %v, want %v: each image is built for what its own app runs on, before anything else can say so", resp.GetContainerArchs(), want)
	}
}

func TestPreflightNamesNoContainerArchitectureForAProviderThatWrapsNone(t *testing.T) {
	t.Parallel()

	client, _ := contractServed(t, "1.2.3")
	resp, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if len(resp.GetContainerArchs()) != 0 {
		t.Errorf("Preflight() container archs = %v, want none from a provider that wraps no containers", resp.GetContainerArchs())
	}
}

func TestACredentialProblemSaysWhatWentWrongByTheRefusalsCode(t *testing.T) {
	t.Parallel()

	for code, want := range map[refusal.Code]string{
		refusal.CodeDenied:   "could not authenticate",
		refusal.CodeNotReady: "could not reach",
		refusal.CodeInvalid:  "misconfigured",
	} {
		refused := refusal.Refuse(code, "ada@box port 22 said so\nFix it")
		problem := providerkit.CredentialProblemProto(fake.Vendor, refused)
		if problem.GetMessage() != want {
			t.Errorf("a %q refusal reads %q, want %q: a host that never answered refused no credential", code, problem.GetMessage(), want)
		}
		if problem.GetHint() != "ada@box port 22 said so\nFix it" {
			t.Errorf("a %q refusal hints %q, want the refusal's own wording", code, problem.GetHint())
		}
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
	provider.FakeBootstrap().AtSchema(providerkit.BootstrapSchema + 1)

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
	seedStack(t, provider, edge.ClassProduction, "shop", providerkit.EdgeStackState{
		Edge: edge.StackState{Slug: "shop", Class: edge.ClassProduction, Bound: []string{"acme.com"}},
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
	seedStack(t, provider, edge.ClassProduction, "shop", providerkit.EdgeStackState{
		Edge: edge.StackState{Slug: "shop", Class: edge.ClassProduction},
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

func TestPreflightNamesTheEdgeScopeTheEdgeCredentialsReach(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.2.3")
	provider.Edges().(*fake.Edges).Verifies(fake.KindRelay, edge.CredentialIdentity{Account: "acct-42"}, nil)

	resp, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if resp.GetIdentity().GetEdgeScope() != "acct-42" {
		t.Errorf("Preflight() edge scope = %q, want the account the edge credentials answered for", resp.GetIdentity().GetEdgeScope())
	}
	if len(resp.GetCredentialProblems()) != 0 {
		t.Errorf("Preflight() reported %v, want edge credentials that answered to be no problem", resp.GetCredentialProblems())
	}
}

func TestPreflightReportsEdgeCredentialsThatWouldNotAnswer(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.2.3")
	provider.Edges().(*fake.Edges).Verifies(fake.KindRelay, edge.CredentialIdentity{}, errors.New("token expired"))

	resp, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v, want the edge's refusal reported beside the rest of the run", err)
	}
	problems := resp.GetCredentialProblems()
	if len(problems) != 1 {
		t.Fatalf("Preflight() reported %d credential problems, want the edge's own", len(problems))
	}
	if problems[0].GetProvider() != string(fake.KindRelay) {
		t.Errorf("credential problem names %q, want the edge whose credentials were refused", problems[0].GetProvider())
	}
	if !strings.Contains(problems[0].GetMessage(), "could not authenticate: token expired") {
		t.Errorf("credential problem message = %q, want the edge's own wording", problems[0].GetMessage())
	}
	if resp.GetIdentity().GetEdgeScope() != "" {
		t.Errorf("Preflight() edge scope = %q, want none from credentials that never answered", resp.GetIdentity().GetEdgeScope())
	}
	if resp.GetIdentity().GetAccount() == "" {
		t.Error("Preflight() dropped the origin identity over an edge that would not authenticate")
	}
}

func TestPreflightLeavesTheEdgeScopeEmptyWhenNoEdgeVerifiesCredentials(t *testing.T) {
	t.Parallel()

	client, _ := contractServed(t, "1.2.3")

	resp, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if resp.GetIdentity().GetEdgeScope() != "" {
		t.Errorf("Preflight() edge scope = %q, want none: no edge here answers for credentials of its own", resp.GetIdentity().GetEdgeScope())
	}
	if len(resp.GetCredentialProblems()) != 0 {
		t.Errorf("Preflight() reported %v, want an edge that verifies nothing to be no problem", resp.GetCredentialProblems())
	}
}

func TestPreflightNamesNoEdgeScopeForAnEdgeThatChecksNoCredentials(t *testing.T) {
	t.Parallel()

	client, provider := contractServed(t, "1.2.3")
	front, err := provider.Edges().Open(fake.KindRelay)
	if err != nil {
		t.Fatal(err)
	}
	if front.Hooks().VerifyCredentials != nil {
		t.Fatal("the reference edge checks its credentials, so it cannot stand for one that checks none")
	}

	resp, err := client.Preflight(context.Background(), &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if resp.GetIdentity().GetEdgeScope() != "" {
		t.Errorf("Preflight() edge scope = %q, want none from an edge that checks no credentials", resp.GetIdentity().GetEdgeScope())
	}
	if len(resp.GetCredentialProblems()) != 0 {
		t.Errorf("Preflight() reported %v, want nothing from an edge that checks no credentials", resp.GetCredentialProblems())
	}
}
