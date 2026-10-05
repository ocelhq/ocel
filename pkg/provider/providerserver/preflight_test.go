package providerserver_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"sync"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestPreflightReportsWhoThisRunIsAndWhatItIncludes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureCache},
	})
	recordProject(t, vendor, "blog")

	resp, err := preflight(ctx, client, &contractv1.PreflightRequest{
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
		t.Errorf("Preflight() = %v/%v, want the production bootstrap it just installed", resp.GetInfrastructurePresent(), resp.GetInfraTier())
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

	vendor := fake.NewProvider(fake.Options{Region: "nowhere"})
	client := servedBy(t, vendor.WrappingContainers("arm64", []byte("runtime")))

	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
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
	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
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
		problem := providerserver.CredentialProblemProto(fake.Vendor, refused)
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

	client, vendor := contractServed(t, "1.2.3")
	vendor.Credentials().(*fake.Credentials).Deny("run `fake login` and try again")

	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
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

func questionIn(err error) *contractv1.Question {
	var wire *connect.Error
	if !errors.As(err, &wire) {
		return nil
	}
	for _, detail := range wire.Details() {
		if value, err := detail.Value(); err == nil {
			if question, ok := value.(*contractv1.Question); ok {
				return question
			}
		}
	}
	return nil
}

func askingCredentials(vendor *fake.Provider, confirmed *int) {
	creds := vendor.Credentials().(*fake.Credentials)
	creds.Ask("the host key is in no known_hosts file; record it and try again", provider.Question{
		Finding: "the host key for 203.0.113.7 is in none of ~/.ssh/known_hosts",
		Prompt:  "Trust that key?",
		Confirm: func(context.Context) error {
			*confirmed++
			creds.Admit()
			return nil
		},
	})
}

func TestPreflightRefusesWithTheQuestionTheProviderAskedAndConfirmRunsWhatAYesDoes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, vendor := contractServed(t, "1.2.3")
	confirmed := 0
	askingCredentials(vendor, &confirmed)

	_, err := preflight(ctx, client, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PRODUCTION})
	question := questionIn(err)
	if question == nil || question.GetId() == "" {
		t.Fatalf("Preflight() error = %v, want a refusal carrying a question with an id to answer", err)
	}
	if question.GetFinding() == "" || question.GetPrompt() != "Trust that key?" {
		t.Errorf("question = %v, want the provider's finding and prompt", question)
	}
	if !strings.Contains(err.Error(), "record it and try again") {
		t.Errorf("Preflight() error = %v, want the remedy for a caller nobody can answer for", err)
	}
	if confirmed != 0 {
		t.Fatal("the provider acted on a question nobody answered")
	}

	if _, err := client.Confirm(ctx, &contractv1.ConfirmRequest{QuestionId: question.GetId()}); err != nil {
		t.Fatalf("Confirm() error = %v", err)
	}
	if confirmed != 1 {
		t.Errorf("Confirm() ran the provider's action %d times, want once", confirmed)
	}
	if _, err := preflight(ctx, client, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PRODUCTION}); err != nil {
		t.Errorf("Preflight() after the answer = %v, want the retried call to go through", err)
	}
	if _, err := client.Confirm(ctx, &contractv1.ConfirmRequest{QuestionId: question.GetId()}); connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("a second Confirm() of one question = %v, want it refused: a question is answered once", err)
	}
}

func TestConfirmRefusesAQuestionTheProviderNeverAsked(t *testing.T) {
	t.Parallel()

	client, _ := contractServed(t, "1.2.3")
	_, err := client.Confirm(context.Background(), &contractv1.ConfirmRequest{QuestionId: "never-asked"})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Errorf("Confirm() = %v, want InvalidArgument: only the provider's own questions can be confirmed", err)
	}
}

func TestPreflightReportsAPlainCredentialRefusalAsAProblemAndAsksNothing(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	vendor.Credentials().(*fake.Credentials).Deny("the host key for 203.0.113.7 changed")

	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PRODUCTION})
	if err != nil || questionIn(err) != nil {
		t.Fatalf("Preflight() error = %v, want the refusal reported in the answer", err)
	}
	if len(resp.GetCredentialProblems()) != 1 {
		t.Errorf("Preflight() reported %v, want it as the one credential problem", resp.GetCredentialProblems())
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

	resp, err := preflight(ctx, client, &contractv1.PreflightRequest{
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

func TestPreflightWithNoBootstrapYetReportsEveryFeatureTheProjectNeeds(t *testing.T) {
	t.Parallel()

	client, _ := contractServed(t, "1.2.3")
	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Frameworks:   []string{"next"},
		Edge:         &contractv1.EdgeSelection{Kind: "direct"},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	var absent []string
	for _, stack := range resp.GetBootstrap().GetStacks() {
		if stack.GetFeature() != "" && stack.GetRequired() && !stack.GetPresent() {
			absent = append(absent, stack.GetFeature())
		}
	}
	if want := []string{fake.FeatureCache, fake.FeatureImages}; !slices.Equal(absent, want) {
		t.Errorf("Preflight() reports %v as required and absent, want %v, which a next project needs", absent, want)
	}
}

func TestPreflightReportsEveryFeatureTheProjectNeedsThatABootstrapLacks(t *testing.T) {
	t.Parallel()

	client, _ := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: &contractv1.EdgeSelection{Kind: "direct"}})
	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Frameworks:   []string{"next"},
		Edge:         &contractv1.EdgeSelection{Kind: "direct"},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	var absent []string
	for _, stack := range resp.GetBootstrap().GetStacks() {
		if stack.GetFeature() != "" && stack.GetRequired() && !stack.GetPresent() {
			absent = append(absent, stack.GetFeature())
		}
	}
	if want := []string{fake.FeatureCache, fake.FeatureImages}; !slices.Equal(absent, want) {
		t.Errorf("Preflight() reports %v as required and absent, want %v, which a next project needs and the bootstrap lacks", absent, want)
	}
}

func TestPreflightReturnsTheGlobalPreviewWildcard(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, _ := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PREVIEW})

	resp, err := preflight(ctx, client, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PREVIEW})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if resp.GetPreviewWildcard() != nil {
		t.Errorf("Preflight() = %+v, want no wildcard before one is used", resp.GetPreviewWildcard())
	}

	if result := usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q, want the wildcard raised", result.GetError())
	}

	resp, err = preflight(ctx, client, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PREVIEW})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	wildcard := resp.GetPreviewWildcard()
	if wildcard.GetBaseDomain() != "preview.acme.com" {
		t.Fatalf("Preflight() wildcard = %+v, want the recorded base domain", wildcard)
	}
	if !wildcard.GetRouteInstalled() {
		t.Error("Preflight() says the shared entry route is not installed, though the edge owns it")
	}
}

func TestPreflightFallsBackToTheSiblingTier(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, _ := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})

	resp, err := preflight(ctx, client, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PREVIEW})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if !resp.GetInfrastructurePresent() || resp.GetInfraTier() != environmentv1.Tier_TIER_PRODUCTION {
		t.Errorf("Preflight() = %v/%v, want it to name the production bootstrap that is installed", resp.GetInfrastructurePresent(), resp.GetInfraTier())
	}
	if resp.GetBootstrap().GetPresent() {
		t.Error("Preflight() reports a preview bootstrap that was never installed")
	}
}

func TestPreflightNamesWhoAlreadyServesEachHostnameThisProjectDeclares(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).Owns("acme.com", "ocel-other-production")

	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
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

	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	seedStack(t, vendor, environment.TierProduction, "shop", stackrecords.EdgeState{
		Edge: edge.StackState{Slug: "shop", Tier: environment.TierProduction, Bound: []string{"acme.com"}},
	})
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).Owns("acme.com", "ocel-shop-production")

	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
		Domains:      []string{"acme.com"},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	claims := resp.GetDomainClaims()
	if len(claims) != 1 || claims[0].GetStatus() != contractv1.DomainClaim_STATUS_UNCLAIMED {
		t.Fatalf("Preflight() reported %+v for a hostname this project already serves, want it unclaimed: a redeploy would otherwise be refused for serving its own domain", claims)
	}
}

func TestPreflightDoesNotRefuseAHostnameThisProjectAlreadyClaimsButNeverRecorded(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	seedStack(t, vendor, environment.TierProduction, "shop", stackrecords.EdgeState{
		Edge: edge.StackState{Slug: "shop", Tier: environment.TierProduction},
	})
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).Owns("acme.com", "ocel-shop-production")

	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
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

	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION})
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).OwnersUnreadable(errors.New("the edge was throttled listing what it serves"))

	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
		Domains:      []string{"acme.com"},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v, want a deploy that continues: who serves a hostname is advisory, and a provider that hiccups enumerating owners has said nothing about this project", err)
	}
	claims := resp.GetDomainClaims()
	if len(claims) != 1 || claims[0].GetStatus() != contractv1.DomainClaim_STATUS_UNSPECIFIED {
		t.Fatalf("Preflight() reported %+v for a hostname whose owner could not be read, want it unanswered rather than claimed or cleared", claims)
	}
	if !strings.Contains(claims[0].GetCause(), "throttled") {
		t.Errorf("claim cause = %q, want the reason the owner could not be read: a guard that goes quiet without saying why is one nobody can tell from a hostname nobody owns", claims[0].GetCause())
	}
}

func TestPreflightTreatsTheSharedPreviewEntryAsNobodysClaim(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PREVIEW})
	wildcard := edge.PreviewWildcard("previews.example.com")
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).Owns(wildcard, edge.PreviewEntryOwner)

	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
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

	client, vendor := contractServed(t, "1.2.3")
	vendor.Edges().(*fake.Edges).Verifies(fake.KindRelay, edge.CredentialIdentity{Account: "acct-42"}, nil)

	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
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

	client, vendor := contractServed(t, "1.2.3")
	vendor.Edges().(*fake.Edges).Verifies(fake.KindRelay, edge.CredentialIdentity{}, errors.New("token expired"))

	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
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

	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
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

	client, vendor := contractServed(t, "1.2.3")
	front, err := vendor.Edges().Open(fake.KindRelay, nil)
	if err != nil {
		t.Fatal(err)
	}
	if front.Hooks().VerifyCredentials != nil {
		t.Fatal("the reference edge checks its credentials, so it cannot represent one that checks none")
	}

	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
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

func TestPreflightSaysWhetherTheTierNeedsAHostnameToServeOn(t *testing.T) {
	t.Parallel()

	t.Run("a router that serves only on hostnames it is given needs one", func(t *testing.T) {
		t.Parallel()
		client, _ := contractServed(t, "1.2.3")
		resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PRODUCTION})
		if err != nil {
			t.Fatalf("Preflight() error = %v", err)
		}
		if !resp.GetHostnameRequired() {
			t.Error("Preflight() says no hostname is needed, want one: the fake router serves only hostnames it is given")
		}
	})

	t.Run("a router that addresses itself needs none", func(t *testing.T) {
		t.Parallel()
		client, vendor := contractServed(t, "1.2.3")
		vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).AddressesItself(true)
		resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PRODUCTION})
		if err != nil {
			t.Fatalf("Preflight() error = %v", err)
		}
		if resp.GetHostnameRequired() {
			t.Error("Preflight() says a hostname is needed, want none: the router gives every app an address of its own")
		}
	})
}

func dnsSelection(kind provider.DNSKind) *contractv1.EdgeSelection {
	return &contractv1.EdgeSelection{Dns: &contractv1.Dns{Kind: string(kind), Zone: "acme.com"}}
}

func TestPreflightReportsDNSCredentialsThatFailVerification(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	vendor.DNS().(*fake.DNS).Verifies(errors.New("token revoked"))

	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Edge:         dnsSelection(fake.KindZone),
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v, want the DNS refusal reported beside the rest of the run", err)
	}
	problems := resp.GetCredentialProblems()
	if len(problems) != 1 {
		t.Fatalf("Preflight() reported %v, want the DNS writer's own credential problem", problems)
	}
	if problems[0].GetProvider() != string(fake.KindZone) {
		t.Errorf("credential problem names %q, want the DNS writer whose credentials were refused", problems[0].GetProvider())
	}
	if !strings.Contains(problems[0].GetMessage(), "could not authenticate: token revoked") {
		t.Errorf("credential problem message = %q, want the DNS writer's own wording", problems[0].GetMessage())
	}
	if resp.GetIdentity().GetAccount() == "" {
		t.Error("Preflight() dropped the origin identity over DNS credentials that would not authenticate")
	}
}

func TestPreflightChecksNoDNSCredentialsWhenTheProjectSelectsNoDNS(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	vendor.DNS().(*fake.DNS).Verifies(errors.New("token revoked"))

	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if len(resp.GetCredentialProblems()) != 0 {
		t.Errorf("Preflight() reported %v, want no DNS check for a project that writes no records", resp.GetCredentialProblems())
	}
}

func TestPreflightRefusesADNSWriterTheProviderDoesNotHave(t *testing.T) {
	t.Parallel()

	client, _ := contractServed(t, "1.2.3")

	_, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Edge:         dnsSelection("no-such-dns"),
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("Preflight() error = %v, want the unknown DNS writer refused as invalid", err)
	}
}

type edgeVendorDNS struct {
	*fake.Provider
	dns *fake.DNS
}

func (v edgeVendorDNS) DNS() provider.DNS { return v }

func (v edgeVendorDNS) Open(_ provider.DNSKind, zone string, front edge.Kind) (edge.DNSRecords, error) {
	return v.dns.Open(fake.KindZone, zone, front)
}

func TestPreflightReportsCredentialsTheEdgeAndItsDNSShareOnce(t *testing.T) {
	t.Parallel()

	dns := fake.NewDNS()
	dns.Verifies(errors.New("token revoked"))
	vendor := edgeVendorDNS{Provider: fake.NewProvider(fake.Options{Region: "nowhere"}).WithProjectDir(workingDir(t)), dns: dns}
	vendor.Edges().(*fake.Edges).Verifies(fake.KindRelay, edge.CredentialIdentity{}, errors.New("token revoked"))
	client := servedProvider(t, "1.2.3", vendor)

	selection := dnsSelection(provider.DNSKind(fake.KindRelay))
	selection.Kind = string(fake.KindRelay)
	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Edge:         selection,
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if problems := resp.GetCredentialProblems(); len(problems) != 1 {
		t.Errorf("Preflight() reported %v, want the one problem the edge and its DNS writer share", problems)
	}
}

func TestPreflightChecksNoDNSCredentialsTheEdgeOfTheSameVendorAlreadyPassed(t *testing.T) {
	t.Parallel()

	dns := fake.NewDNS()
	dns.Verifies(errors.New("token revoked"))
	vendor := edgeVendorDNS{Provider: fake.NewProvider(fake.Options{Region: "nowhere"}).WithProjectDir(workingDir(t)), dns: dns}
	vendor.Edges().(*fake.Edges).Verifies(fake.KindRelay, edge.CredentialIdentity{Account: "edge-account"}, nil)
	client := servedProvider(t, "1.2.3", vendor)

	selection := dnsSelection(provider.DNSKind(fake.KindRelay))
	selection.Kind = string(fake.KindRelay)
	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Edge:         selection,
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if problems := resp.GetCredentialProblems(); len(problems) != 0 {
		t.Errorf("Preflight() reported %v, want none: the edge's passing check covers its own vendor's DNS writer", problems)
	}
	if got := resp.GetIdentity().GetEdgeScope(); got != "edge-account" {
		t.Errorf("Preflight() edge scope = %q, want the account the edge's check answered with", got)
	}
}

type frontRefusingDNS struct {
	*fake.Provider
}

func (v frontRefusingDNS) DNS() provider.DNS { return v }

func (v frontRefusingDNS) Open(kind provider.DNSKind, zone string, front edge.Kind) (edge.DNSRecords, error) {
	if front == fake.KindRelay {
		return nil, refusal.Refuse(refusal.CodeInvalid, "%s cannot write the records a %s edge answers on", kind, front)
	}
	return v.Provider.DNS().Open(kind, zone, front)
}

func TestPreflightRefusesADNSWriterThatCannotServeTheSelectedEdge(t *testing.T) {
	t.Parallel()

	vendor := frontRefusingDNS{Provider: fake.NewProvider(fake.Options{Region: "nowhere"}).WithProjectDir(workingDir(t))}
	client := servedProvider(t, "1.2.3", vendor)

	selection := dnsSelection(fake.KindZone)
	selection.Kind = string(fake.KindRelay)
	_, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Edge:         selection,
	})
	if connect.CodeOf(err) != connect.CodeInvalidArgument || !strings.Contains(err.Error(), "cannot write the records a relay edge answers on") {
		t.Fatalf("Preflight() error = %v, want the DNS writer's refusal of the edge it would front, before the build", err)
	}
}

func TestPreflightNamesNoKnownSlugsForAProjectThatIsAlreadyRecorded(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureCache},
	})
	recordProject(t, vendor, "blog")
	recordProject(t, vendor, "shop")

	resp, err := preflight(ctx, client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if len(resp.GetKnownSlugs()) != 0 {
		t.Errorf("Preflight() known slugs = %v, want none for a project the backend already records", resp.GetKnownSlugs())
	}
}

type projectListingProvider struct {
	*fake.Provider

	mu       sync.Mutex
	listings int
}

func (p *projectListingProvider) KeyValues() keyvalue.Store {
	return projectListingStore{Store: p.Provider.KeyValues(), on: p}
}

func (p *projectListingProvider) projectListings() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.listings
}

type projectListingStore struct {
	keyvalue.Store
	on *projectListingProvider
}

func (s projectListingStore) List(ctx context.Context, in keyvalue.Partition, under ...string) ([]keyvalue.Entry, error) {
	if in.Root == keyvalue.RootProjects {
		s.on.mu.Lock()
		s.on.listings++
		s.on.mu.Unlock()
	}
	return s.Store.List(ctx, in, under...)
}

func TestPreflightOfARecordedProjectReadsItsRecordWithoutListingTheOthers(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	vendor := &projectListingProvider{Provider: fake.NewProvider(fake.Options{Region: "nowhere"}).WithProjectDir(workingDir(t))}
	client := servedProvider(t, "1.2.3", vendor)
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier:     environmentv1.Tier_TIER_PRODUCTION,
		Features: []string{fake.FeatureCache},
	})
	recordProject(t, vendor.Provider, "blog")
	recordProject(t, vendor.Provider, "shop")
	before := vendor.projectListings()

	if _, err := preflight(ctx, client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
	}); err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if listed := vendor.projectListings() - before; listed != 0 {
		t.Errorf("Preflight() listed the projects partition %d times, want none for a project whose own record answers whether it is new", listed)
	}
}
