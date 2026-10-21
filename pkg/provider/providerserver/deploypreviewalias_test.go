package providerserver_test

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func dryEnsurePreviewAlias(t *testing.T, client contractv1connect.ProviderServiceClient) map[string]string {
	t.Helper()
	ensured, err := client.EnsurePreviewAlias(context.Background(), &contractv1.EnsurePreviewAliasRequest{
		Slug:        "shop",
		Environment: previewRequest().GetEnvironment(),
		Token:       "abcdefghijklmnop",
		Apps:        []string{"web"},
		Domains:     []string{"*.preview.example"},
		Dry:         true,
	})
	if err != nil {
		t.Fatalf("EnsurePreviewAlias: %v", err)
	}
	return ensured.GetUrls()
}

func TestADryEnsurePreviewAliasOfANewPreviewAnswersNoHostnameAndRecordsNothing(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)

	if hostnames := dryEnsurePreviewAlias(t, client); len(hostnames) != 0 {
		t.Errorf("a dry EnsurePreviewAlias of a preview never deployed = %v, want no hostname: its alias is assigned on its first deploy, and one signed now would never exist", hostnames)
	}
	for _, key := range []records.Key{stackrecords.NewPreviewKeyRecordKey(), stackrecords.EnvironmentKey(environment.TierPreview, "shop", "pr-7")} {
		if _, err := vendor.KeyValues().Read(context.Background(), key); !errors.Is(err, records.ErrNotFound) {
			t.Errorf("reading %s after a dry run = %v, want nothing written", key, err)
		}
	}
}

func TestADryEnsurePreviewAliasOfADeployedPreviewAnswersTheAliasItIsServedOn(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	previewBootstrapped(t, client)
	result, _ := deploy(t, client, previewRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	if got, want := dryEnsurePreviewAlias(t, client)["web"], servedURLs(result)[0]; got != want {
		t.Errorf("a dry EnsurePreviewAlias of a deployed preview answered %q, want %q, the alias it is served on", got, want)
	}
}

func TestADryDeployOfANewPreviewSaysItsHostnameIsAssignedOnItsFirstDeploy(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	previewBootstrapped(t, client)
	req := previewRequest()
	req.Dry = true

	result, events := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy(dry) = %q", result.GetError())
	}
	said := false
	for _, event := range events {
		if strings.Contains(event.GetMessage(), "assigned on its first deploy") {
			said = true
		}
		if strings.Contains(event.GetMessage(), "pr-7-") {
			t.Errorf("the dry deploy said %q, want no hostname for a preview never deployed: one signed now would never exist", event.GetMessage())
		}
	}
	if !said {
		t.Error("the dry deploy of a new preview never said its hostname is assigned on its first deploy")
	}
}

func ensureAlias(t *testing.T, client contractv1connect.ProviderServiceClient, token string) {
	t.Helper()
	if _, err := client.EnsurePreviewAlias(context.Background(), &contractv1.EnsurePreviewAliasRequest{
		Slug:        "shop",
		Environment: previewRequest().GetEnvironment(),
		Token:       token,
		Apps:        []string{"web"},
		Domains:     []string{"*.preview.example"},
	}); err != nil {
		t.Fatalf("EnsurePreviewAlias: %v", err)
	}
}

func forgetAlias(t *testing.T, client contractv1connect.ProviderServiceClient, token string) {
	t.Helper()
	if _, err := client.ForgetPreviewAlias(context.Background(), &contractv1.ForgetPreviewAliasRequest{
		Slug:        "shop",
		Environment: previewRequest().GetEnvironment(),
		Token:       token,
	}); err != nil {
		t.Fatalf("ForgetPreviewAlias: %v", err)
	}
}

func isPreviewRecorded(t *testing.T, vendor *fake.Provider) bool {
	t.Helper()
	_, err := vendor.KeyValues().Read(context.Background(), stackrecords.EnvironmentKey(environment.TierPreview, "shop", "pr-7"))
	if err != nil && !errors.Is(err, records.ErrNotFound) {
		t.Fatal(err)
	}
	return err == nil
}

func TestAPreviewRefusedForAnOverlongLabelIsNeverRecorded(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	if result := usePreviewWildcard(t, client, "preview.acme.com", edged(fake.KindRelay, "acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q", result.GetError())
	}
	slug := strings.Repeat("s", 45)
	recorded := func() bool {
		t.Helper()
		_, err := vendor.KeyValues().Read(context.Background(), stackrecords.EnvironmentKey(environment.TierPreview, slug, "pr-7"))
		if err != nil && !errors.Is(err, records.ErrNotFound) {
			t.Fatal(err)
		}
		return err == nil
	}

	req := previewDeployRequest()
	req.Manifest.Slug = slug
	if result, _, err := deployStream(t, client, req); err == nil && result.GetSuccess() {
		t.Fatal("Deploy() of a label over 63 characters succeeded")
	}
	if recorded() {
		t.Error("Deploy() refused the label but recorded the preview, want nothing for `ocel preview ls` to list")
	}

	if _, err := client.EnsurePreviewAlias(context.Background(), &contractv1.EnsurePreviewAliasRequest{
		Slug: slug, Environment: previewDeployRequest().GetEnvironment(), Token: "abcdefghijklmnop", Apps: []string{"web"},
	}); err == nil {
		t.Fatal("EnsurePreviewAlias() of a label over 63 characters succeeded")
	}
	if recorded() {
		t.Error("EnsurePreviewAlias() refused the label but recorded the preview, want nothing for `ocel preview ls` to list")
	}
}

func TestForgetPreviewAliasRemovesTheAliasANeverDeployedPreviewWasAssigned(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	ensureAlias(t, client, "abcdefghijklmnop")

	forgetAlias(t, client, "abcdefghijklmnop")

	if isPreviewRecorded(t, vendor) {
		t.Error("pr-7 is still recorded after its alias was forgotten before any deploy claimed it, want nothing left for `ocel preview ls` to list")
	}
}

func TestForgetPreviewAliasKeepsAPreviewAnotherRunAssignedOrADeployClaimed(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	ensureAlias(t, client, "abcdefghijklmnop")

	forgetAlias(t, client, "qrstuvwxyzabcdef")
	if !isPreviewRecorded(t, vendor) {
		t.Fatal("forgetting an alias token pr-7 was never assigned removed its record, want it kept for the run that was")
	}

	if result, _ := deploy(t, client, previewRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	listed, err := client.ListEnvironments(context.Background(), &contractv1.ListEnvironmentsRequest{Slug: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	token := strings.TrimPrefix(listed.GetEnvironments()[0].GetAliasUrls()[0], "https://pr-7-")[:edge.PreviewTokenLen]
	forgetAlias(t, client, token)
	if !isPreviewRecorded(t, vendor) {
		t.Error("forgetting the alias of a deployed preview removed its record, want a preview a deploy claimed kept until `ocel preview rm`")
	}
}

func TestEnsurePreviewAliasAnswersTheURLsThePreviewDeployIsServedOn(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	previewBootstrapped(t, client)

	ensured, err := client.EnsurePreviewAlias(context.Background(), &contractv1.EnsurePreviewAliasRequest{
		Slug:        "shop",
		Environment: previewRequest().GetEnvironment(),
		Token:       "abcdefghijklmnop",
		Apps:        []string{"web"},
		Domains:     []string{"*.preview.example"},
	})
	if err != nil {
		t.Fatalf("EnsurePreviewAlias: %v", err)
	}
	url := ensured.GetUrls()["web"]
	if !strings.HasPrefix(url, "https://pr-7-abcdefghijklmnop") {
		t.Fatalf("EnsurePreviewAlias = %v, want web on pr-7 and the token the CLI minted", ensured.GetUrls())
	}

	result, _ := deploy(t, client, previewRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if want := []string{url}; !slices.Equal(servedURLs(result), want) {
		t.Errorf("the deploy announced %v, want %v: the build baked in the alias it was answered", servedURLs(result), want)
	}

	again, err := client.EnsurePreviewAlias(context.Background(), &contractv1.EnsurePreviewAliasRequest{
		Slug:        "shop",
		Environment: previewRequest().GetEnvironment(),
		Token:       "qrstuvwxyz234567",
		Apps:        []string{"web"},
		Domains:     []string{"*.preview.example"},
	})
	if err != nil {
		t.Fatalf("EnsurePreviewAlias again: %v", err)
	}
	if again.GetUrls()["web"] != url {
		t.Errorf("EnsurePreviewAlias again = %v, want the alias the preview already has", again.GetUrls())
	}
	if again.GetToken() != "abcdefghijklmnop" {
		t.Errorf("EnsurePreviewAlias again answered the token %q, want abcdefghijklmnop, the one the preview was created with, for its deploy to carry", again.GetToken())
	}
}

func deployBuiltFor(t *testing.T, client contractv1connect.ProviderServiceClient, token string) *progressv1.OperationResult {
	t.Helper()
	req := previewRequest()
	req.AliasToken = token
	result, _ := deploy(t, client, req)
	return result
}

func TestAPreviewDeployRecreatesTheAliasItsBuildWasGivenWhenAFailedRunForgotIt(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	previewBootstrapped(t, client)
	ensureAlias(t, client, "abcdefghijklmnop")
	forgetAlias(t, client, "abcdefghijklmnop")

	result := deployBuiltFor(t, client, "abcdefghijklmnop")

	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if urls := servedURLs(result); len(urls) != 1 || !strings.HasPrefix(urls[0], "https://pr-7-abcdefghijklmnop") {
		t.Errorf("the deploy is served on %v, want the alias abcdefghijklmnop its build baked in", urls)
	}
}

func TestAPreviewKeepsTheAliasItsBuildWasGivenWhenAFailedRunForgetsItBeforeTheDeployClaimsThePreview(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	ensureAlias(t, client, "abcdefghijklmnop")
	vendor.WithHooks(func(hooks *provider.Hooks) {
		hooks.Preflight = &provider.PreflightHooks{Plan: vendor.PlanPreflight, Apply: func(context.Context, provider.DeployPreflight) error {
			_, err := client.ForgetPreviewAlias(context.Background(), &contractv1.ForgetPreviewAliasRequest{
				Slug: "shop", Environment: previewRequest().GetEnvironment(), Token: "abcdefghijklmnop",
			})
			return err
		}}
	})

	if result := deployBuiltFor(t, client, "abcdefghijklmnop"); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	vendor.WithHooks(func(hooks *provider.Hooks) { hooks.Preflight = nil })
	next := deployBuiltFor(t, client, "")

	if !next.GetSuccess() {
		t.Fatalf("the next Deploy() = %q", next.GetError())
	}
	if urls := servedURLs(next); len(urls) != 1 || !strings.HasPrefix(urls[0], "https://pr-7-abcdefghijklmnop") {
		t.Errorf("the next deploy is served on %v, want the alias abcdefghijklmnop the first deploy was served on", urls)
	}
	meta, err := stackrecords.ReadEnvironmentMeta(context.Background(), vendor.KeyValues(), environment.TierPreview, "shop", "pr-7")
	if err != nil {
		t.Fatal(err)
	}
	if meta.AliasToken != "abcdefghijklmnop" {
		t.Errorf("pr-7 records the alias token %q, want abcdefghijklmnop, the one its deploys were served on", meta.AliasToken)
	}
	for _, host := range meta.Aliases {
		if !strings.Contains(host.Hostname, "abcdefghijklmnop") {
			t.Errorf("pr-7 records the alias %s, want every alias on the token abcdefghijklmnop", host.Hostname)
		}
	}
}

func TestAPreviewDeployRecreatesTheAliasItsBuildWasGivenWhenThePreviewWasRemovedMeanwhile(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	previewBootstrapped(t, client)
	if result := deployBuiltFor(t, client, ""); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	ensureAlias(t, client, "abcdefghijklmnop")
	built := readListedAliasToken(t, client)
	removing, err := client.RemoveEnvironment(context.Background(), &contractv1.RemoveEnvironmentRequest{
		Slug: "shop", Environment: &environmentv1.Environment{
			Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7", Lifecycle: environmentv1.Lifecycle_LIFECYCLE_PERSISTENT,
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(removing); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveEnvironment() = %q, %v", result.GetError(), err)
	}

	result := deployBuiltFor(t, client, built)

	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if urls := servedURLs(result); len(urls) != 1 || !strings.HasPrefix(urls[0], "https://pr-7-"+built) {
		t.Errorf("the deploy is served on %v, want the alias %s its build baked in", urls, built)
	}
}

func readListedAliasToken(t *testing.T, client contractv1connect.ProviderServiceClient) string {
	t.Helper()
	listed, err := client.ListEnvironments(context.Background(), &contractv1.ListEnvironmentsRequest{Slug: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	return strings.TrimPrefix(listed.GetEnvironments()[0].GetAliasUrls()[0], "https://pr-7-")[:edge.PreviewTokenLen]
}

func TestAPreviewDeployIsRefusedWhenThePreviewWasGivenAnotherAliasSinceItsBuild(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)
	previewBootstrapped(t, client)
	ensureAlias(t, client, "abcdefghijklmnop")
	forgetAlias(t, client, "abcdefghijklmnop")
	ensureAlias(t, client, "qrstuvwxyz234567")

	result := deployBuiltFor(t, client, "abcdefghijklmnop")

	if result.GetSuccess() || !strings.Contains(result.GetError(), "another alias") {
		t.Errorf("Deploy() = %v %q, want it refused: its build baked the alias abcdefghijklmnop and the preview is now on qrstuvwxyz234567", result.GetSuccess(), result.GetError())
	}
	if served := plane.ListServedHostnames(); len(served) != 0 {
		t.Errorf("%v are served, want nothing: a build is never served on hostnames it was not built for", served)
	}
}

func TestDeployAnnouncesThePreviewHostnameOfTheGlobalWildcard(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	previewBootstrapped(t, client)
	if result := usePreviewWildcard(t, client, "preview.acme.com", edged(fake.KindRelay, "acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q", result.GetError())
	}

	result, _ := deploy(t, client, previewDeployRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	urls := servedURLs(result)
	if len(urls) != 1 || !regexp.MustCompile(`^https://shop-[a-z2-7]{24}\.preview\.acme\.com$`).MatchString(urls[0]) {
		t.Errorf("the preview deploy announced %v, want the project's slug and a signed token on the global wildcard, and never the preview's name", urls)
	}
}

func TestDeployAnnouncesAPreviewHostnamePerAppWhenTheProjectHasMoreThanOne(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	previewBootstrapped(t, client)
	if result := usePreviewWildcard(t, client, "preview.acme.com", edged(fake.KindRelay, "acme.com")); !result.GetSuccess() {
		t.Fatalf("UsePreviewWildcard() = %q", result.GetError())
	}

	req := twoAppRequest()
	req.Manifest.Domains = nil
	req.Environment = &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-7"}
	req.Edge = &contractv1.EdgeSelection{Kind: string(fake.KindRelay)}

	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	web, admin := servedAppURLs(result, "web"), servedAppURLs(result, "admin")
	if len(web) != 1 || len(admin) != 1 || web[0] == admin[0] {
		t.Errorf("the preview deploy announced %v for web and %v for admin, want one hostname per app: the appless hostname is ambiguous once a project has two apps",
			web, admin)
	}
}

func TestDeployRefusesAProductionProjectThatDeclaresNoHostname(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := deployRequest()
	req.Manifest.Domains = nil

	stream, err := client.Deploy(context.Background(), rooted(req))
	if err != nil {
		t.Fatal(err)
	}
	var failure string
	for stream.Receive() {
		if result := stream.Msg().GetResult(); result != nil {
			failure = result.GetError()
		}
	}
	said := failure + connectMessage(stream.Err())
	stream.Close()

	if !strings.Contains(said, "domains.production") {
		t.Fatalf("Deploy() of a project declaring no hostname = %q, want it refused for the domain it does not declare", said)
	}
}

func TestDeployServesAHostnameDeclaredOnAnAppRatherThanTheProject(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := deployRequest()
	req.Manifest.Domains = nil
	req.Manifest.Apps[0].Domains = []*contractv1.TierDomains{{
		Tier:      environmentv1.Tier_TIER_PRODUCTION,
		Hostnames: []string{"shop.example"},
	}}

	req.Edge = writtenBy("shop.example")

	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() of a project whose only hostname sits on an app = %q, want it admitted", result.GetError())
	}
	if !slices.Equal(servedURLs(result), []string{"https://shop.example"}) {
		t.Errorf("the deploy returned urls %v, want the app-declared hostname it attached printed", servedURLs(result))
	}
}

func TestDeployAnnouncesEachAppsOwnHostnameUnderThatApp(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)

	req := twoAppRequest()
	req.Manifest.Domains = nil
	req.Manifest.Apps[0].Domains = []*contractv1.TierDomains{{
		Tier:      environmentv1.Tier_TIER_PRODUCTION,
		Hostnames: []string{"shop.example"},
	}}
	req.Manifest.Apps[1].Domains = []*contractv1.TierDomains{{
		Tier:      environmentv1.Tier_TIER_PRODUCTION,
		Hostnames: []string{"admin.shop.example"},
	}}
	req.Edge = writtenBy("shop.example")

	result, _ := deploy(t, client, req)
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	if got := servedAppURLs(result, "web"); !slices.Equal(got, []string{"https://shop.example"}) {
		t.Errorf("web has %v, want the hostname web itself declares", got)
	}
	if got := servedAppURLs(result, "admin"); !slices.Equal(got, []string{"https://admin.shop.example"}) {
		t.Errorf("admin has %v, want the hostname admin itself declares", got)
	}
}
