package providerserver_test

import (
	"context"
	"errors"
	"maps"
	"slices"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/trace"

	"github.com/ocelhq/ocel/pkg/arch"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/processenv"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/providerserver"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/stackrecords"
	"github.com/ocelhq/ocel/pkg/tracing"
	"github.com/ocelhq/ocel/pkg/tracing/tracingtest"
)

func TestPreflightReportsWhoThisRunIsAndWhatItIncludes(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
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
		refusal.CodeBusy:     "rate limited",
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

func TestPreflightRefusesAnOptionWhoamiRefusedRatherThanReportingTheCredentials(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	vendor.Credentials().(*fake.Credentials).RejectOption(`option "region" names "nowher", which is not a region fake offers`)

	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
	})
	if code, refused := provider.RefusedCode(err); !refused || code != refusal.CodeInvalid {
		t.Fatalf("Preflight() = %v, %v, want an %s refusal: an option is refused for what it names, whatever call met it first", resp, err, refusal.CodeInvalid)
	}
	if !strings.Contains(err.Error(), `option "region" names "nowher"`) {
		t.Errorf("Preflight() refused with %q, want the option's own wording", err)
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
		Remedy:  "ssh-keyscan -t ssh-ed25519 -p 22 203.0.113.7 >> ~/.ssh/known_hosts",
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

func TestPreflightRequiresThePartsTheEdgeNeeds(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, _ := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{
		Tier: environmentv1.Tier_TIER_PRODUCTION,
	})

	resp, err := preflight(ctx, client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Edge:         &contractv1.EdgeSelection{Kind: "relay"},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	for _, stack := range resp.GetBootstrap().GetStacks() {
		if stack.GetPart() == fake.PartCache && !stack.GetRequired() {
			t.Errorf("%s is not marked required, and the relay edge needs it", fake.PartCache)
		}
	}
}

func TestPreflightWithNoBootstrapYetReportsEveryPartTheEdgeRequires(t *testing.T) {
	t.Parallel()

	client, _ := contractServed(t, "1.2.3")
	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Edge:         &contractv1.EdgeSelection{Kind: "relay"},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	var absent []string
	for _, stack := range resp.GetBootstrap().GetStacks() {
		if stack.GetPart() != "" && stack.GetRequired() && !stack.GetPresent() {
			absent = append(absent, stack.GetPart())
		}
	}
	if want := []string{fake.PartImages}; !slices.Equal(absent, want) {
		t.Errorf("Preflight() reports %v as required and absent, want %v, which the relay edge requires", absent, want)
	}
}

func TestPreflightReportsEveryPartTheEdgeRequiresThatABootstrapLacks(t *testing.T) {
	t.Parallel()

	client, _ := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: &contractv1.EdgeSelection{Kind: "direct"}})
	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Edge:         &contractv1.EdgeSelection{Kind: "relay"},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	var absent []string
	for _, stack := range resp.GetBootstrap().GetStacks() {
		if stack.GetPart() != "" && stack.GetRequired() && !stack.GetPresent() {
			absent = append(absent, stack.GetPart())
		}
	}
	if want := []string{fake.PartImages}; !slices.Equal(absent, want) {
		t.Errorf("Preflight() reports %v as required and absent, want %v, which the relay edge requires and the bootstrap lacks", absent, want)
	}
}

func wantedParts(t *testing.T, vendor *fake.Provider, tier environment.Tier) map[string][]string {
	t.Helper()
	wants, err := stackrecords.ListBootstrapWants(context.Background(), vendor.KeyValues(), tier)
	if err != nil {
		t.Fatal(err)
	}
	return wants
}

func TestAPreflightAskedToRecordWantsRecordsEveryPartTheProjectWantsThatTheBootstrapLacks(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	direct := &contractv1.EdgeSelection{Kind: "direct"}
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	bootstrapped(t, vendor, environment.TierProduction, fake.PartCache)
	_, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
		Edge:         &contractv1.EdgeSelection{Kind: "relay"},
		RecordWants:  true,
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}

	want := map[string][]string{fake.PartImages: {"shop"}}
	if got := wantedParts(t, vendor, environment.TierProduction); !maps.EqualFunc(got, want, slices.Equal) {
		t.Errorf("wants = %v, want %v: the bootstrap holds cache, and the relay edge also wants images", got, want)
	}
}

func TestAPreflightRecordsWantsAndResolvesTheProvidersOwnRegistryWhileAStepNamesIt(t *testing.T) {
	t.Parallel()

	client, vendor, calls := timedServed(t)
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: &contractv1.EdgeSelection{Kind: "direct"}})
	vendor.WithHooks(func(hooks *provider.Hooks) {
		hooks.EnsureImageRegistry = func(context.Context, environment.Tier) (provider.RegistryTarget, error) {
			defer calls.time("resolve the provider's own image registry", time.Now())
			return ownRegistry, nil
		}
	})
	calls.reset()

	_, steps, err := preflightWithSteps(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
		Edge:         &contractv1.EdgeSelection{Kind: "relay"},
		RecordWants:  true,
		Containers:   []*contractv1.ContainerApp{{App: "web"}},
	})

	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if unnamed := callsNoSpanNamed(steps, calls.list()); len(unnamed) > 0 {
		t.Errorf("the preflight made these calls while no step named what it waited on: %v", unnamed)
	}
	if got := wantedParts(t, vendor, environment.TierProduction); !slices.Equal(got[fake.PartImages], []string{"shop"}) {
		t.Errorf("wants = %v, want shop to want %s", got, fake.PartImages)
	}
}

func TestAPreflightRecordsAPartTheCommandWantsBeyondItsBuildOnlyWhenTheProviderOffersIt(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	direct := &contractv1.EdgeSelection{Kind: "direct"}
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	_, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
		Edge:         direct,
		RecordWants:  true,
		WantedParts:  []string{fake.PartCache, "variables-key"},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}

	want := map[string][]string{fake.PartCache: {"shop"}}
	if got := wantedParts(t, vendor, environment.TierProduction); !maps.EqualFunc(got, want, slices.Equal) {
		t.Errorf("wants = %v, want %v: this provider offers no variables-key", got, want)
	}
}

func TestAPreflightNotAskedToRecordWantsRecordsNone(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	direct := &contractv1.EdgeSelection{Kind: "direct"}
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PRODUCTION, Edge: direct})
	_, err := preflight(context.Background(), client, &contractv1.PreflightRequest{
		RequiredTier: environmentv1.Tier_TIER_PRODUCTION,
		Slug:         "shop",
		Edge:         direct,
		WantedParts:  []string{fake.PartCache},
	})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}

	if got := wantedParts(t, vendor, environment.TierProduction); len(got) != 0 {
		t.Errorf("wants = %v, want none: a dry run or a read changes nothing", got)
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

	resp, err = preflight(ctx, client, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PREVIEW, CheckPreviewRoute: true})
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

func TestPreflightTakesThePreviewEntryRouteAsInstalledWithoutAskingTheEdgeWhenTheChecksAreSkipped(t *testing.T) {
	for _, skipped := range []bool{false, true} {
		if skipped {
			t.Setenv(processenv.SkipChecksEnvVar, "1")
		}
		ctx := context.Background()
		client, vendor := contractServed(t, "1.2.3")
		bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PREVIEW})
		if result := usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com")); !result.GetSuccess() {
			t.Fatalf("UsePreviewWildcard() = %q, want the wildcard raised", result.GetError())
		}
		vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).OwnersUnreadable(errors.New("the edge was throttled listing what it serves"))

		resp, err := preflight(ctx, client, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PREVIEW, CheckPreviewRoute: true})
		if !skipped {
			if err == nil {
				t.Errorf("Preflight() with the checks run = %+v, want the edge asked and its failure to read the owner returned", resp.GetPreviewWildcard())
			}
			continue
		}
		if err != nil {
			t.Fatalf("Preflight() with the checks skipped error = %v, want the edge never asked", err)
		}
		if !resp.GetPreviewWildcard().GetRouteInstalled() {
			t.Error("Preflight() with the checks skipped says the shared entry route is not installed, want it taken as installed: only an edge asked who owns it can say it is not")
		}
	}
}

func TestAPreflightThatDoesNotCheckThePreviewRouteReadsNoOwnerFromTheEdge(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PREVIEW})
	if result := usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q, want the wildcard raised", result.GetError())
	}
	front := vendor.Edges().(*fake.Edges).Edge(fake.KindRelay)
	before := front.OwnerReads()

	resp, err := preflight(ctx, client, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PREVIEW})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}

	if resp.GetPreviewWildcard().GetBaseDomain() != "preview.acme.com" {
		t.Errorf("Preflight() wildcard = %+v, want the recorded base domain", resp.GetPreviewWildcard())
	}
	if read := front.OwnerReads() - before; read != 0 {
		t.Errorf("a preflight that does not check the preview route read the edge's route owners %d times, want none", read)
	}
}

func TestPreflightSaysTheGlobalPreviewWildcardsRouteIsNotInstalledWhenTheEdgeAnswersThatNobodyOwnsIt(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PREVIEW})
	if result := usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q, want the wildcard raised", result.GetError())
	}
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).Owns(edge.PreviewWildcard("preview.acme.com"), "")

	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PREVIEW, CheckPreviewRoute: true})
	if err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}
	if resp.GetPreviewWildcard().GetRouteInstalled() {
		t.Error("Preflight() says the shared entry route is installed, though the edge answers that nobody owns it")
	}
}

func TestPreflightFailsWithTheReasonWhenTheGlobalPreviewWildcardsRouteCannotBeRead(t *testing.T) {
	t.Parallel()

	client, vendor := contractServed(t, "1.2.3")
	bootstrapOK(t, client, &contractv1.BootstrapRequest{Tier: environmentv1.Tier_TIER_PREVIEW})
	if result := usePreviewWildcard(t, client, "preview.acme.com", zoned("acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q, want the wildcard raised", result.GetError())
	}
	vendor.Edges().(*fake.Edges).Edge(fake.KindRelay).OwnersUnreadable(errors.New("the edge was rate limited reading its routes"))

	resp, err := preflight(context.Background(), client, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PREVIEW, CheckPreviewRoute: true})
	if err == nil {
		t.Fatalf("Preflight() = %+v, want an error: a route that could not be read is not a route that is missing", resp.GetPreviewWildcard())
	}
	if !strings.Contains(err.Error(), "rate limited") {
		t.Errorf("Preflight() error = %v, want the reason the route could not be read", err)
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

type tracedCredentials struct {
	provider.Credentials
}

func (c tracedCredentials) Whoami(ctx context.Context) (provider.Principal, error) {
	_, span := otel.Tracer("vendor").Start(ctx, "sts.GetCallerIdentity")
	defer span.End()
	return c.Credentials.Whoami(ctx)
}

type tracedVendor struct {
	*fake.Provider
}

func (p tracedVendor) Credentials() provider.Credentials {
	return tracedCredentials{p.Provider.Credentials()}
}

func TestWorkInsideAPreflightStepIsTracedUnderThatStepInEachCallersTrace(t *testing.T) {
	recording, recorded := tracingtest.Record(t)
	traceID := trace.SpanContextFromContext(recording).TraceID().String()

	client := servedBy(t, tracedVendor{fake.NewProvider(fake.Options{Region: "nowhere"})})
	nestedUnder := map[string]string{}
	for _, caller := range []string{"00f067aa0ba902b7", "1111111111111111"} {
		ctx, call := connect.NewClientContext(context.Background())
		call.RequestHeader().Set("traceparent", "00-"+traceID+"-"+caller+"-01")
		_, events, err := preflightWithSteps(ctx, client, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PRODUCTION})
		if err != nil {
			t.Fatalf("Preflight() error = %v", err)
		}
		var step []byte
		for _, event := range events {
			if event.GetStarted() != nil && strings.Contains(event.GetMessage(), "credentials") {
				step = event.GetSpanId()
			}
		}
		if step == nil {
			t.Fatalf("Preflight() started no step checking the credentials: %v", events)
		}
		callerID, _ := trace.SpanIDFromHex(caller)
		nestedUnder[caller] = tracing.StepSpanID(callerID, step).String()
	}
	if nestedUnder["00f067aa0ba902b7"] == nestedUnder["1111111111111111"] {
		t.Fatal("one step in two calls has one span ID in the trace")
	}

	var parents []string
	for _, span := range recorded.Ended() {
		if span.Name() != "sts.GetCallerIdentity" {
			continue
		}
		parents = append(parents, span.Parent().SpanID().String())
	}
	want := []string{nestedUnder["00f067aa0ba902b7"], nestedUnder["1111111111111111"]}
	if !slices.Equal(parents, want) {
		t.Errorf("the vendor's calls nest under %v, want each call's step checking the credentials %v", parents, want)
	}
}

func TestEachCallIsAServerSpanUnderTheCallersClientSpan(t *testing.T) {
	recording, recorded := tracingtest.Record(t)
	traceID := trace.SpanContextFromContext(recording).TraceID().String()

	client := servedBy(t, fake.NewProvider(fake.Options{Region: "nowhere"}))
	ctx, call := connect.NewClientContext(context.Background())
	call.RequestHeader().Set("traceparent", "00-"+traceID+"-00f067aa0ba902b7-01")
	if _, _, err := preflightWithSteps(ctx, client, &contractv1.PreflightRequest{RequiredTier: environmentv1.Tier_TIER_PRODUCTION}); err != nil {
		t.Fatalf("Preflight() error = %v", err)
	}

	for _, span := range recorded.Ended() {
		if span.Name() != strings.TrimPrefix(contractv1connect.ProviderServicePreflightProcedure, "/") {
			continue
		}
		if span.SpanKind() != trace.SpanKindServer {
			t.Errorf("the call's span is a %s span, want a server span", span.SpanKind())
		}
		if got := span.Parent().SpanID().String(); got != "00f067aa0ba902b7" {
			t.Errorf("the call's span nests under %s, want the caller's client span", got)
		}
		return
	}
	t.Fatal("the call is not a span")
}
