package box_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/fake"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const previewBase = "preview.example.com"

func previewSpec() edge.PreviewWildcardSpec {
	return edge.PreviewWildcardSpec{
		BaseDomain: previewBase,
		GrammarMin: edge.PreviewGrammarMin,
		GrammarMax: edge.PreviewGrammarMax,
	}
}

func TestThePreviewEntryBearsNoCertificateAndStillPublishesAFrontToPointAt(t *testing.T) {
	t.Parallel()

	_, front, _ := standing(t)
	ctx := context.Background()
	spec := previewSpec()
	if spec.Certificate != "" {
		t.Fatalf("this test is meant to reconcile a wildcard carrying no certificate and carries %q", spec.Certificate)
	}

	published, err := front.ReconcilePreviewWildcard(ctx, spec)
	if err != nil {
		t.Fatalf("ReconcilePreviewWildcard with no certificate = %v: a box terminates each preview hostname on its own http-01 certificate, so there is no wildcard certificate for this spec to carry", err)
	}
	if published != address {
		t.Fatalf("the wildcard published %q, want the box's address %q: %s resolves to one A record and it is the box", published, address, edge.PreviewWildcard(previewBase))
	}
	records, err := edge.RecordsFor(edge.DNSTarget{Kind: front.Kind(), Front: published}, []string{edge.PreviewWildcard(previewBase)})
	if err != nil {
		t.Fatalf("RecordsFor: %v", err)
	}
	want := edge.Record{Name: edge.PreviewWildcard(previewBase), Type: edge.RecordTypeA, Value: address}
	if len(records) != 1 || records[0] != want {
		t.Errorf("records = %v, want %v: a box's floor is one manual A record for the whole preview base", records, want)
	}
}

func TestTheWildcardIsOwnedByThePreviewEntryWhileItsRouteStandsAndByNobodyAfter(t *testing.T) {
	t.Parallel()

	_, front, _ := standing(t)
	ctx := context.Background()
	wildcard := edge.PreviewWildcard(previewBase)

	if owner, err := front.DomainOwner(ctx, wildcard); err != nil || owner != "" {
		t.Fatalf("DomainOwner(%s) = %q, %v before anything installed it, want nobody", wildcard, owner, err)
	}
	if _, err := front.ReconcilePreviewWildcard(ctx, previewSpec()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}
	owner, err := front.DomainOwner(ctx, wildcard)
	if err != nil {
		t.Fatalf("DomainOwner: %v", err)
	}
	if owner != edge.PreviewEntryOwner {
		t.Errorf("DomainOwner(%s) = %q, want %q: `ocel domain status` reads this to decide whether the wildcard route is installed, and a false answer prints MISSING on a box that is serving",
			wildcard, owner, edge.PreviewEntryOwner)
	}
	if err := front.DestroyPreviewWildcard(ctx, previewBase); err != nil {
		t.Fatalf("DestroyPreviewWildcard: %v", err)
	}
	if owner, err := front.DomainOwner(ctx, wildcard); err != nil || owner != "" {
		t.Errorf("DomainOwner(%s) = %q, %v once the route is gone, want nobody", wildcard, owner, err)
	}
}

func TestAPreviewWildcardWithNoBaseIsRefusedRatherThanInstalledAsADefaultRoute(t *testing.T) {
	t.Parallel()

	_, front, _ := standing(t)
	spec := previewSpec()
	spec.BaseDomain = ""

	_, err := front.ReconcilePreviewWildcard(context.Background(), spec)
	if err == nil {
		t.Fatal("a preview wildcard naming no base domain was installed, and the route it renders carries no host matcher: it receives every hostname pointed at this machine, a mistyped production hostname included")
	}
	if !strings.Contains(err.Error(), "host matcher") {
		t.Errorf("the refusal reads %q, and it is the empty-host default route it must name", err)
	}
}

func TestABoxAlreadyServingOnePreviewBaseRefusesASecondRatherThanSwappingIt(t *testing.T) {
	t.Parallel()

	_, front, _ := standing(t)
	ctx := context.Background()
	if _, err := front.ReconcilePreviewWildcard(ctx, previewSpec()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}
	other := previewSpec()
	other.BaseDomain = "previews.example.org"

	if _, err := front.ReconcilePreviewWildcard(ctx, other); err == nil {
		t.Fatal("a second preview base was installed over the first, and every preview hostname on this box is a name under the base it was raised on: swapping it silently takes every live preview off the air")
	}
}

func previewStack(t *testing.T, stood *machine) edge.EdgeStack {
	t.Helper()

	front := box.New(stood, fake.NewRecords(), sshScope)
	if _, err := front.ReconcilePreviewWildcard(context.Background(), previewSpec()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}
	stack, err := front.Reconcile(context.Background(), edge.StackSpec{
		Version: "test", Class: edge.ClassPreview, Slug: slug,
	}, edge.StackState{GlobalPreview: previewBase})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return stack
}

func previewed(t *testing.T, stack edge.EdgeStack, pointer string, apps ...string) {
	t.Helper()

	builds := map[string]string{}
	for _, app := range apps {
		staged(t, stack, app, "b1", slug+"-"+app+"-"+pointer)
		builds[app] = "b1"
	}
	if err := stack.Promote(context.Background(), edge.Promotion{
		PromotionID: "p-" + pointer, Ts: 1, Builds: builds,
	}, pointer, edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote(%s): %v", pointer, err)
	}
}

func claimedOn(t *testing.T, stood *machine) []host.HostClaim {
	t.Helper()

	held, err := stood.Claims(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return held
}

func TestAPreviewOfAMultiAppProjectClaimsOneHostnamePerApp(t *testing.T) {
	t.Parallel()

	stood := aMachine()
	previewed(t, previewStack(t, stood), "pr-7", "api", "web")

	surface := box.Surface(slug, edge.ClassPreview)
	want := []host.HostClaim{
		{Hostname: slug + "--pr-7--api." + previewBase, Owner: surface, Pointer: "pr-7", App: "api"},
		{Hostname: slug + "--pr-7--web." + previewBase, Owner: surface, Pointer: "pr-7", App: "web"},
	}
	if held := claimedOn(t, stood); !slices.Equal(held, want) {
		t.Fatalf("the box holds %v, want %v: a preview is one routing entry per app the project has, each on its own hostname and its own per-host certificate", held, want)
	}
}

func TestAPreviewOfASingleAppProjectClaimsTheOneHostnameTheBranchIsNamedFor(t *testing.T) {
	t.Parallel()

	stood := aMachine()
	previewed(t, previewStack(t, stood), "pr-7", "web")

	want := []host.HostClaim{{
		Hostname: slug + "--pr-7." + previewBase,
		Owner:    box.Surface(slug, edge.ClassPreview),
		Pointer:  "pr-7",
	}}
	if held := claimedOn(t, stood); !slices.Equal(held, want) {
		t.Fatalf("the box holds %v, want %v", held, want)
	}
}

func TestTwoBranchesOfOneProjectEachKeepTheirOwnPreviewHostname(t *testing.T) {
	t.Parallel()

	stood := aMachine()
	stack := previewStack(t, stood)
	previewed(t, stack, "pr-7", "web")
	previewed(t, stack, "pr-9", "web")

	held := claimedOn(t, stood)
	if len(held) != 2 {
		t.Fatalf("the box holds %v, want one hostname per live branch: the preview hostname is a function of the branch name, so a second branch is a second name rather than a second deploy of the first", held)
	}
	for _, claim := range held {
		if !strings.Contains(claim.Hostname, "--"+claim.Pointer+".") {
			t.Errorf("%s is claimed under branch %q", claim.Hostname, claim.Pointer)
		}
	}
}

func TestRemovingAPreviewPointerTakesItsHostnamesOffTheBoxWithIt(t *testing.T) {
	t.Parallel()

	stood := aMachine()
	stack := previewStack(t, stood)
	previewed(t, stack, "pr-7", "api", "web")
	previewed(t, stack, "pr-9", "api", "web")

	if _, err := stack.RemovePointer(context.Background(), "pr-7", edge.DiscardReporter()); err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}
	for _, claim := range claimedOn(t, stood) {
		if claim.Pointer == "pr-7" {
			t.Errorf("%s is still claimed on this box after the preview it belongs to was removed: the box's proxy holds a certificate per hostname, and a name nothing serves keeps being renewed", claim.Hostname)
		}
		if claim.Pointer != "pr-9" {
			t.Errorf("removing one preview took %s with it, and it belongs to branch %q", claim.Hostname, claim.Pointer)
		}
	}
	if len(claimedOn(t, stood)) != 2 {
		t.Errorf("the box holds %v after one of two branches went, want the other branch's two hostnames", claimedOn(t, stood))
	}
}

func TestAPreviewHostnameDnsWillNotCarryIsRefusedRatherThanClaimed(t *testing.T) {
	t.Parallel()

	stood := aMachine()
	stack := previewStack(t, stood)
	over := strings.Repeat("b", edge.PreviewLabelMaxLen)
	staged(t, stack, "web", "b1", slug+"-web-1")

	err := stack.Promote(context.Background(), edge.Promotion{
		PromotionID: "p-over", Ts: 1, Builds: map[string]string{"web": "b1"},
	}, over, edge.DiscardReporter())
	if err == nil {
		t.Fatalf("a preview whose hostname carries a %d-character label was claimed on this box: DNS caps a label at %d, so the name resolves nowhere and its acme order can never succeed. The check lives in the CLI's preflight alone, and a caller that skips preflight reaches this",
			len(slug)+len(edge.PreviewAppSeparator)+len(over), edge.PreviewLabelMaxLen)
	}
	if held := claimedOn(t, stood); len(held) != 0 {
		t.Errorf("the refusal still left %v claimed on the box", held)
	}
}

func TestAPreviewClaimsTheHostnamesThePreviewSiteItselfNames(t *testing.T) {
	t.Parallel()

	for what, apps := range map[string][]string{
		"one app":  {"web"},
		"two apps": {"api", "web"},
	} {
		stood := aMachine()
		previewed(t, previewStack(t, stood), "pr-7", apps...)

		site := edge.SharedPreview(slug, previewBase)
		want := site.Hosts("pr-7", apps)
		var held []string
		for _, claim := range claimedOn(t, stood) {
			held = append(held, claim.Hostname)
			if claim.App != "" && site.Host("pr-7", claim.App) != claim.Hostname {
				t.Errorf("%s: %s is claimed under app %q, and that is not the app the hostname is built from", what, claim.Hostname, claim.App)
			}
		}
		slices.Sort(held)
		slices.Sort(want)
		if !slices.Equal(held, want) {
			t.Errorf("%s: the box claims %v and the preview site names %v: the rule that a project of one app serves one hostname with no app segment lives in PreviewSite.Hosts, and a second copy of it here is one drift away from a CLI printing a URL this box does not route",
				what, held, want)
		}
	}
}

func TestAProductionPromotionClaimsNoPreviewHostnameAtAll(t *testing.T) {
	t.Parallel()

	stood, front, stack := standing(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	if err := promoted(t, stack, "p1", "web", "b1"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if held := claimedOn(t, stood); len(held) != 0 {
		t.Errorf("a production promotion claimed %v", held)
	}

	pointed, err := front.Reconcile(context.Background(), edge.StackSpec{
		Version: "test", Class: edge.ClassProduction, Slug: slug,
	}, edge.StackState{GlobalPreview: previewBase})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	staged(t, pointed, "web", "b2", "shop-web-2222")
	if err := pointed.Promote(context.Background(), edge.Promotion{
		PromotionID: "p2", Ts: 2, Builds: map[string]string{"web": "b2"},
	}, "pr-7", edge.DiscardReporter()); err != nil {
		t.Fatalf("Promote under a pointer: %v", err)
	}
	if held := claimedOn(t, stood); len(held) != 0 {
		t.Errorf("a production promotion under pointer pr-7 on a box that knows a preview base claimed %v: the class is the whole of what decides whether a promotion claims a preview hostname, and the pointer and the base alone do not", held)
	}
}

func TestRemovingAPreviewPointerTakesTheCertificatesBehindItsHostnamesWithIt(t *testing.T) {
	t.Parallel()

	stood := aMachine()
	stack := previewStack(t, stood)
	previewed(t, stack, "pr-7", "api", "web")
	previewed(t, stack, "pr-9", "api", "web")

	site := edge.SharedPreview(slug, previewBase)
	want := site.Hosts("pr-7", []string{"api", "web"})
	slices.Sort(want)

	if _, err := stack.RemovePointer(context.Background(), "pr-7", edge.DiscardReporter()); err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}
	if !slices.Equal(stood.forgotten, want) {
		t.Fatalf("the teardown forgot %v, want %v: the proxy holds one certificate and one private key per hostname it ever terminated, so a teardown that takes the routes and leaves the pairs leaves bytes behind and grows with previews-ever", stood.forgotten, want)
	}
	for _, hostname := range site.Hosts("pr-9", []string{"api", "web"}) {
		if slices.Contains(stood.forgotten, hostname) {
			t.Errorf("%s was forgotten with another branch's preview, and it is still being served", hostname)
		}
	}
}

func TestAPreviewWhoseGlobalBaseWentMissingStillForgetsTheCertificatesItsClaimsName(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	stood := aMachine()
	front := box.New(stood, fake.NewRecords(), sshScope)
	if _, err := front.ReconcilePreviewWildcard(ctx, previewSpec()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}
	spec := edge.StackSpec{Version: "test", Class: edge.ClassPreview, Slug: slug}
	raised, err := front.Reconcile(ctx, spec, edge.StackState{GlobalPreview: previewBase})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	previewed(t, raised, "pr-7", "web")

	want := make([]string, 0, 1)
	for _, claim := range claimedOn(t, stood) {
		want = append(want, claim.Hostname)
	}
	slices.Sort(want)
	if len(want) == 0 {
		t.Fatal("the promotion claimed no hostname, so this teardown has no certificate to leave behind and the assertion below is vacuous")
	}

	zeroed, err := front.Reconcile(ctx, spec, edge.StackState{})
	if err != nil {
		t.Fatalf("Reconcile with no global preview base: %v", err)
	}
	if _, err := zeroed.RemovePointer(ctx, "pr-7", edge.DiscardReporter()); err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}

	if !slices.Equal(stood.forgotten, want) {
		t.Errorf("the teardown forgot %v, want %v: the class alone decides whether a pointer holds preview hostnames, and the state's preview base is zeroed for every pointer of the class the moment one deploy stops being served on the shared base. Reading it here leaves the certificate and key of every hostname this pointer still claims sitting in the proxy's data",
			stood.forgotten, want)
	}
}

func TestATeardownThatFellOverLeavesThePointersHistoryStandingForTheNextRun(t *testing.T) {
	t.Parallel()

	stood := aMachine()
	stack := previewStack(t, stood)
	previewed(t, stack, "pr-7", "api", "web")
	stood.refuse = errors.New("the proxy answered nothing over its admin socket")

	if _, err := stack.RemovePointer(context.Background(), "pr-7", edge.DiscardReporter()); err == nil {
		t.Fatal("a teardown whose box calls all refused reported success")
	}

	history, err := stack.Ledger().History(context.Background(), "pr-7")
	if err != nil {
		t.Fatalf("History: %v", err)
	}
	if len(history) == 0 {
		t.Fatal("a teardown that fell over took the pointer's history with it: every step this method can fail on runs before the ledger write, because the history is the only thing that names what the next run has left to reach")
	}
}

func TestRemovingAPointerNothingWasEverPromotedUnderTakesNothingAndRefusesNothing(t *testing.T) {
	t.Parallel()

	stood := aMachine()
	stack := previewStack(t, stood)

	if _, err := stack.RemovePointer(context.Background(), "pr-7", edge.DiscardReporter()); err != nil {
		t.Fatalf("RemovePointer of a preview that is already gone = %v, and teardown is run again on every retry", err)
	}
	if len(stood.forgotten) != 0 {
		t.Errorf("a preview that claimed nothing forgot %v", stood.forgotten)
	}
}

func TestRemovingAPreviewLeavesTheCatchAllStandingAndRendersItAsKeptWithAReason(t *testing.T) {
	t.Parallel()

	stood := aMachine()
	stack := previewStack(t, stood)
	previewed(t, stack, "pr-7", "web")
	front := box.New(stood, fake.NewRecords(), sshScope)

	if _, err := stack.RemovePointer(context.Background(), "pr-7", edge.DiscardReporter()); err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}

	held, err := front.DomainOwner(context.Background(), edge.PreviewWildcard(previewBase))
	if err != nil {
		t.Fatalf("DomainOwner: %v", err)
	}
	if held != edge.PreviewEntryOwner {
		t.Fatalf("the catch-all is owned by %q after a preview came down, want %q: it is a bootstrap item answering for every project this box serves, and taking it with one project's preview takes every other project's previews off the air",
			held, edge.PreviewEntryOwner)
	}
	kept := front.SharedPreviewRemoval()
	if kept.Action != edge.PlanKeep || kept.Reason == "" {
		t.Errorf("the catch-all renders as %+v, want a kept row carrying why it is kept", kept)
	}
}

func TestATeardownThatFailedOnTheCertificatesForgetsThemOnTheNextRun(t *testing.T) {
	t.Parallel()

	stood := aMachine()
	stack := previewStack(t, stood)
	previewed(t, stack, "pr-7", "api", "web")

	stood.refuse = errors.New("the proxy answered nothing over its admin socket")
	if _, err := stack.RemovePointer(context.Background(), "pr-7", edge.DiscardReporter()); err == nil {
		t.Fatal("a teardown whose certificate removal failed reported success, and the pairs it left are bytes nothing else will ever take")
	}
	stood.refuse = nil
	stood.forgotten = nil

	if _, err := stack.RemovePointer(context.Background(), "pr-7", edge.DiscardReporter()); err != nil {
		t.Fatalf("the second run = %v", err)
	}

	site := edge.SharedPreview(slug, previewBase)
	want := site.Hosts("pr-7", []string{"api", "web"})
	slices.Sort(want)
	if !slices.Equal(stood.forgotten, want) {
		t.Errorf("the second run forgot %v, want %v: the first run disclaimed the hostnames before it fell over, so a teardown that reads them off the claims alone can never reach them again",
			stood.forgotten, want)
	}
}
