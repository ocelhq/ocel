package box_test

import (
	"context"
	"crypto/sha256"
	"encoding/base32"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/box"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const previewBase = "preview.example.com"

func previewSpec() edge.PreviewWildcardSpec {
	return edge.PreviewWildcardSpec{BaseDomain: previewBase}
}

const previewKey edge.PreviewKey = "box-preview-key"

func listPreviewHosts(pointer string, apps ...string) []edge.PreviewHost {
	sum := sha256.Sum256([]byte(pointer))
	token := base32.NewEncoding("abcdefghijklmnopqrstuvwxyz234567").EncodeToString(sum[:])[:edge.PreviewTokenLen]
	return edge.NewSharedPreviewSite(slug, previewBase, previewKey).ListHosts(pointer, token, apps)
}

func TestThePreviewEntryBearsNoCertificateAndStillPublishesAFrontToPointAt(t *testing.T) {
	t.Parallel()

	_, front, _ := reconciled(t)
	ctx := context.Background()
	spec := previewSpec()
	if spec.Certificate != "" {
		t.Fatalf("this test is meant to reconcile a wildcard with no certificate and has %q", spec.Certificate)
	}

	published, err := front.ReconcilePreviewWildcard(ctx, spec)
	if err != nil {
		t.Fatalf("ReconcilePreviewWildcard with no certificate = %v: a box terminates each preview hostname on its own http-01 certificate, so there is no wildcard certificate for this spec to include", err)
	}
	if published != address {
		t.Fatalf("the wildcard published %q, want the box's address %q: %s resolves to one A record and it is the box", published, address, edge.PreviewWildcard(previewBase))
	}
	records, err := edge.RecordsFor(edge.DNSTarget{Kind: front.Kind(), Address: published}, []string{edge.PreviewWildcard(previewBase)})
	if err != nil {
		t.Fatalf("RecordsFor: %v", err)
	}
	want := edge.Record{Name: edge.PreviewWildcard(previewBase), Type: edge.RecordTypeA, Value: address}
	if len(records) != 1 || records[0] != want {
		t.Errorf("records = %v, want %v: a box's floor is one manual A record for the whole preview base", records, want)
	}
}

func TestTheWildcardIsOwnedByThePreviewEntryWhileItsRouteExistsAndByNobodyAfter(t *testing.T) {
	t.Parallel()

	_, front, _ := reconciled(t)
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

	_, front, _ := reconciled(t)
	spec := previewSpec()
	spec.BaseDomain = ""

	_, err := front.ReconcilePreviewWildcard(context.Background(), spec)
	if err == nil {
		t.Fatal("a preview wildcard naming no base domain was installed, and the route it renders has no host matcher: it receives every hostname pointed at this machine, a mistyped production hostname included")
	}
	if !strings.Contains(err.Error(), "no base domain") {
		t.Errorf("the refusal reads %q, want the missing base domain named", err)
	}
}

func TestABoxAlreadyServingOnePreviewBaseRefusesASecondRatherThanSwappingIt(t *testing.T) {
	t.Parallel()

	_, front, _ := reconciled(t)
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

func previewStack(t *testing.T, m *machine) boxStack {
	t.Helper()
	return previewStackOn(t, edgeOver(m, fake.NewKeyValues()))
}

func previewStackOn(t *testing.T, front boxEdge) boxStack {
	t.Helper()

	if _, err := front.ReconcilePreviewWildcard(context.Background(), previewSpec()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}
	stackEdge, err := front.Reconcile(context.Background(), edge.StackSpec{
		Version: "test", Tier: environment.TierPreview, Slug: slug,
	}, edge.StackState{GlobalPreview: previewBase})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	return stackOn(front, stackEdge)
}

func previewed(t *testing.T, stack boxStack, pointer string, apps ...string) {
	t.Helper()

	builds := map[string]string{}
	for _, app := range apps {
		staged(t, stack, app, "b-"+pointer, slug+"-"+app+"-"+pointer)
		builds[app] = "b-" + pointer
	}
	movePointerOnto(t, stack, pointer, "p-"+pointer, builds, listPreviewHosts(pointer, apps...))
}

func movePointerOnto(t *testing.T, stack boxStack, pointer, promotionID string, builds map[string]string, hosts []edge.PreviewHost) {
	t.Helper()

	if err := stack.MovePointer(context.Background(), router.PointerMove{Pointer: pointer, Hosts: hosts, Promotion: router.Promotion{
		PromotionID: promotionID, Ts: 1, Releases: builds,
	}}, progress.Discard()); err != nil {
		t.Fatalf("MovePointer(%s onto %s): %v", promotionID, pointer, err)
	}
}

func claimedOn(t *testing.T, m *machine) []host.HostClaim {
	t.Helper()

	claims, err := m.Claims(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	return claims
}

func TestAPreviewOfAMultiAppProjectClaimsEachHostnameTheMoveNamesForItsApp(t *testing.T) {
	t.Parallel()

	m := aMachine()
	previewed(t, previewStack(t, m), "pr-7", "api", "web")

	surface := box.Surface(slug, environment.TierPreview)
	hosts := listPreviewHosts("pr-7", "api", "web")
	want := []host.HostClaim{
		{Hostname: hosts[0].Hostname, Owner: surface, Pointer: "pr-7", App: "api"},
		{Hostname: hosts[1].Hostname, Owner: surface, Pointer: "pr-7", App: "web"},
	}
	slices.SortFunc(want, func(a, b host.HostClaim) int { return strings.Compare(a.Hostname, b.Hostname) })
	claimed := claimedOn(t, m)
	slices.SortFunc(claimed, func(a, b host.HostClaim) int { return strings.Compare(a.Hostname, b.Hostname) })
	if !slices.Equal(claimed, want) {
		t.Fatalf("the box records %v, want %v: a preview is served on the hostnames its move names, each routed to the app it names", claimed, want)
	}
}

func TestAPreviewOfASingleAppProjectClaimsTheOneHostnameTheMoveNames(t *testing.T) {
	t.Parallel()

	m := aMachine()
	previewed(t, previewStack(t, m), "pr-7", "web")

	want := []host.HostClaim{{
		Hostname: listPreviewHosts("pr-7", "web")[0].Hostname,
		Owner:    box.Surface(slug, environment.TierPreview),
		Pointer:  "pr-7",
		App:      "web",
	}}
	if claimed := claimedOn(t, m); !slices.Equal(claimed, want) {
		t.Fatalf("the box records %v, want %v", claimed, want)
	}
}

func TestAPreviewHostnameNeverNamesTheBranch(t *testing.T) {
	t.Parallel()

	m := aMachine()
	previewed(t, previewStack(t, m), "pr-7", "web")

	for _, claim := range claimedOn(t, m) {
		if strings.Contains(claim.Hostname, "pr-7") {
			t.Errorf("%s names the preview it serves, and anyone who knows a branch name could then reach it", claim.Hostname)
		}
	}
}

func TestTwoBranchesOfOneProjectEachKeepTheirOwnPreviewHostname(t *testing.T) {
	t.Parallel()

	m := aMachine()
	stack := previewStack(t, m)
	previewed(t, stack, "pr-7", "web")
	previewed(t, stack, "pr-9", "web")

	claimed := claimedOn(t, m)
	if len(claimed) != 2 {
		t.Fatalf("the box records %v, want one hostname per live branch", claimed)
	}
	for _, claim := range claimed {
		if want := listPreviewHosts(claim.Pointer, "web")[0].Hostname; claim.Hostname != want {
			t.Errorf("%s is claimed under branch %q, whose move named %s", claim.Hostname, claim.Pointer, want)
		}
	}
}

func TestRemovingAPreviewPointerTakesItsHostnamesOffTheBoxWithIt(t *testing.T) {
	t.Parallel()

	m := aMachine()
	stack := previewStack(t, m)
	previewed(t, stack, "pr-7", "api", "web")
	previewed(t, stack, "pr-9", "api", "web")

	if _, err := removePointer(context.Background(), stack, "pr-7", progress.Discard()); err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}
	for _, claim := range claimedOn(t, m) {
		if claim.Pointer == "pr-7" {
			t.Errorf("%s is still claimed on this box after the preview it belongs to was removed: the box's proxy keeps a certificate per hostname, and a name nothing serves keeps being renewed", claim.Hostname)
		}
		if claim.Pointer != "pr-9" {
			t.Errorf("removing one preview took %s with it, and it belongs to branch %q", claim.Hostname, claim.Pointer)
		}
	}
	if len(claimedOn(t, m)) != 2 {
		t.Errorf("the box records %v after one of two branches went, want the other branch's two hostnames", claimedOn(t, m))
	}
}

func TestAMoveReplacesTheHostnamesAPreviewIsServedOn(t *testing.T) {
	t.Parallel()

	m := aMachine()
	stack := previewStack(t, m)
	previewed(t, stack, "pr-7", "api", "web")
	staged(t, stack, "web", "b-2", slug+"-web-2")
	movePointerOnto(t, stack, "pr-7", "p-2", map[string]string{"web": "b-2"}, listPreviewHosts("pr-7", "web"))

	want := listPreviewHosts("pr-7", "web")[0].Hostname
	if claimed := claimedOn(t, m); len(claimed) != 1 || claimed[0].Hostname != want {
		t.Errorf("the box records %v after the preview moved onto %s alone, want that one hostname: a hostname the move no longer names keeps a certificate renewed for a name nothing serves", claimed, want)
	}
}

func TestAPreviewDeploymentKeepsItsHostnameAfterThePreviewMovesOnUntilItIsRemoved(t *testing.T) {
	t.Parallel()

	m := aMachine()
	stack := previewStack(t, m)
	deployment := router.FormatDeploymentPointer("pr-7", "0123456789abcdef0123456789abcdef")
	previewed(t, stack, "pr-7", "web")
	movePointerOnto(t, stack, deployment, "p-pr-7", map[string]string{"web": "b-pr-7"}, listPreviewHosts(deployment, "web"))
	staged(t, stack, "web", "b-2", slug+"-web-2")
	movePointerOnto(t, stack, "pr-7", "p-2", map[string]string{"web": "b-2"}, listPreviewHosts("pr-7", "web"))

	surface := box.Surface(slug, environment.TierPreview)
	if served := m.upstream[host.RouteKey{Owner: surface, Pointer: deployment, App: "web"}]; !strings.HasPrefix(served, slug+"-web-pr-7:") {
		t.Errorf("%s forwards to %q after the preview moved on, want the container it was deployed with", deployment, served)
	}
	own := listPreviewHosts(deployment, "web")[0].Hostname
	if !slices.ContainsFunc(claimedOn(t, m), func(claim host.HostClaim) bool { return claim.Hostname == own && claim.Pointer == deployment }) {
		t.Errorf("the box records %v, want %s still claimed for %s", claimedOn(t, m), own, deployment)
	}

	if err := stack.RemovePointer(context.Background(), router.PointerRemoval{Pointer: deployment, Hosts: listPreviewHosts(deployment, "web")}, progress.Discard()); err != nil {
		t.Fatalf("RemovePointer(%s): %v", deployment, err)
	}
	for _, claim := range claimedOn(t, m) {
		if claim.Pointer != "pr-7" {
			t.Errorf("%s is still claimed for %s after it was removed", claim.Hostname, claim.Pointer)
		}
	}
	if _, routed := m.upstream[host.RouteKey{Owner: surface, Pointer: deployment, App: "web"}]; routed {
		t.Errorf("%s is still routed after it was removed", deployment)
	}
}

func TestAPreviewWithAStoreServesItOnOneLabelUnderTheSameWildcard(t *testing.T) {
	t.Parallel()

	m := aMachine()
	stack := previewStack(t, m)
	surface := box.Surface(slug, environment.TierPreview)
	m.upstream[host.RouteKey{Owner: surface, Pointer: "pr-7", App: switchboard.StoreLabel}] = "shop-pr-7-store-s3:9000"
	previewed(t, stack, "pr-7", "api", "web")

	claimed := claimedHosts(m, switchboard.StoreLabel)
	if len(claimed) != 1 {
		t.Fatalf("the preview claimed %v for its store, want one hostname", claimed)
	}
	label, base, _ := strings.Cut(claimed[0], ".")
	if base != previewBase || len(label) > edge.PreviewLabelMaxLen {
		t.Errorf("the store is claimed on %s, want one DNS label directly under %s: the wildcard's certificate and DNS record cover that and nothing deeper", claimed[0], previewBase)
	}
	for _, app := range listPreviewHosts("pr-7", "api", "web") {
		if claimed[0] == app.Hostname {
			t.Errorf("the store is claimed on %s, which %s is served on", claimed[0], app.App)
		}
	}
	if strings.Contains(claimed[0], "pr-7") {
		t.Errorf("the store is claimed on %s, which names the preview", claimed[0])
	}
	var recorded []live.Claimed
	for _, claim := range claimedOn(t, m) {
		recorded = append(recorded, live.Claimed(claim))
	}
	if base := live.StoreBase(recorded, surface, "pr-7"); base != "https://"+claimed[0] {
		t.Errorf("StoreBase = %q, want https://%s: the agent reads the store's hostname from what the preview claimed", base, claimed[0])
	}

	other := aMachine()
	other.upstream[host.RouteKey{Owner: surface, Pointer: "pr-9", App: switchboard.StoreLabel}] = "shop-pr-9-store-s3:9000"
	previewed(t, previewStack(t, other), "pr-9", "api", "web")
	if elsewhere := claimedHosts(other, switchboard.StoreLabel); slices.Equal(elsewhere, claimed) {
		t.Errorf("two previews both serve their store on %v", claimed)
	}
}

func TestAPreviewStoreHostnameIsNoAppHostnameEvenForAProjectNamedStorage(t *testing.T) {
	t.Parallel()

	m := aMachine()
	front := edgeOver(m, fake.NewKeyValues())
	if _, err := front.ReconcilePreviewWildcard(context.Background(), previewSpec()); err != nil {
		t.Fatalf("ReconcilePreviewWildcard: %v", err)
	}
	named := switchboard.StoreLabel
	stackEdge, err := front.Reconcile(context.Background(), edge.StackSpec{
		Version: "test", Tier: environment.TierPreview, Slug: named,
	}, edge.StackState{GlobalPreview: previewBase})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	stack := stackOn(front, stackEdge)
	m.upstream[host.RouteKey{Owner: box.Surface(named, environment.TierPreview), Pointer: "pr-7", App: switchboard.StoreLabel}] = "storage-pr-7-store-s3:9000"
	staged(t, stack, "web", "b1", named+"-web-1")
	hosts := edge.NewSharedPreviewSite(named, previewBase, previewKey).ListHosts("pr-7", "abcdefghijklmnop", []string{"web"})
	movePointerOnto(t, stack, "pr-7", "p1", map[string]string{"web": "b1"}, hosts)

	if claimed := claimedHosts(m, switchboard.StoreLabel); len(claimed) != 1 || claimed[0] == hosts[0].Hostname {
		t.Errorf("project %q claimed %v for its store, and its app is served on %s: the two must never be one name", named, claimed, hosts[0].Hostname)
	}
}

func TestAPreviewDeploymentClaimsNoStoreOfItsOwn(t *testing.T) {
	t.Parallel()

	m := aMachine()
	stack := previewStack(t, m)
	m.upstream[host.RouteKey{Owner: box.Surface(slug, environment.TierPreview), Pointer: "pr-7", App: switchboard.StoreLabel}] = "shop-pr-7-store-s3:9000"
	previewed(t, stack, "pr-7", "web")
	deployment := router.FormatDeploymentPointer("pr-7", "0123456789abcdef0123456789abcdef")
	movePointerOnto(t, stack, deployment, "p-pr-7", map[string]string{"web": "b-pr-7"}, listPreviewHosts(deployment, "web"))

	if claimed := claimedHosts(m, switchboard.StoreLabel); len(claimed) != 1 {
		t.Errorf("the box claims %v for stores, want the preview's one: its deployments share it", claimed)
	}
}

func TestAProductionPromotionClaimsNoPreviewHostnameAtAll(t *testing.T) {
	t.Parallel()

	m, front, stack := reconciled(t)
	staged(t, stack, "web", "b1", "shop-web-1111")
	if err := promoted(t, stack, "p1", "web", "b1"); err != nil {
		t.Fatalf("Promote: %v", err)
	}
	if claimed := claimedOn(t, m); len(claimed) != 0 {
		t.Errorf("a production promotion claimed %v", claimed)
	}

	pointedEdge, err := front.Reconcile(context.Background(), edge.StackSpec{
		Version: "test", Tier: environment.TierProduction, Slug: slug,
	}, edge.StackState{GlobalPreview: previewBase})
	if err != nil {
		t.Fatalf("Reconcile: %v", err)
	}
	pointed := stackOn(front, pointedEdge)
	staged(t, pointed, "web", "b2", "shop-web-2222")
	if err := pointed.MovePointer(context.Background(), router.PointerMove{Pointer: "pr-7", Promotion: router.Promotion{
		PromotionID: "p2", Ts: 2, Releases: map[string]string{"web": "b2"},
	}}, progress.Discard()); err != nil {
		t.Fatalf("Promote under a pointer: %v", err)
	}
	if claimed := claimedOn(t, m); len(claimed) != 0 {
		t.Errorf("a production promotion under pointer pr-7 on a box that knows a preview base claimed %v: the tier is the whole of what decides whether a promotion claims a preview hostname, and the pointer and the base alone do not", claimed)
	}
}

func callsATeardownMakes(t *testing.T) []string {
	t.Helper()

	m := aMachine()
	stack := previewStack(t, m)
	previewed(t, stack, "pr-7", "api", "web")
	m.visited = nil
	if _, err := removePointer(context.Background(), stack, "pr-7", progress.Discard()); err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}
	reached := slices.DeleteFunc(m.reached(), func(call string) bool { return call == "ApplyOrigins" })
	if len(reached) == 0 {
		t.Fatal("a teardown reached no call on the box that this fake can refuse, so every case below would be vacuous")
	}
	return reached
}

func TestATeardownThatFellOverLeavesThePointersHistoryInPlaceForTheNextRun(t *testing.T) {
	t.Parallel()

	for _, call := range callsATeardownMakes(t) {
		t.Run(call, func(t *testing.T) {
			t.Parallel()

			m := aMachine()
			stack := previewStack(t, m)
			previewed(t, stack, "pr-7", "api", "web")
			m.refuseOn(call, errors.New("the box answered nothing over its ssh session"))

			if _, err := removePointer(context.Background(), stack, "pr-7", progress.Discard()); err == nil {
				t.Fatalf("a teardown whose %s refused reported success, and a step a teardown never makes is a step this table names for nothing", call)
			}
			history, err := stack.Ledger().History(context.Background(), "pr-7")
			if err != nil {
				t.Fatalf("History: %v", err)
			}
			if len(history) == 0 {
				t.Fatalf("a teardown that fell over on %s took the pointer's history with it: the history is the only thing that names what the next run has left to reach, so every step that can fail runs before the ledger write and one moved after it is a step no retry can ever reach", call)
			}
		})
	}
}

func TestRemovingAPointerNothingWasEverPromotedUnderTakesNothingAndRefusesNothing(t *testing.T) {
	t.Parallel()

	m := aMachine()
	stack := previewStack(t, m)

	if _, err := removePointer(context.Background(), stack, "pr-7", progress.Discard()); err != nil {
		t.Fatalf("RemovePointer of a preview that is already gone = %v, and teardown is run again on every retry", err)
	}
}

func TestRemovingAPreviewLeavesTheCatchAllInPlaceAndRendersItAsKeptWithAReason(t *testing.T) {
	t.Parallel()

	m := aMachine()
	stack := previewStack(t, m)
	previewed(t, stack, "pr-7", "web")
	front := edgeOver(m, fake.NewKeyValues())

	if _, err := removePointer(context.Background(), stack, "pr-7", progress.Discard()); err != nil {
		t.Fatalf("RemovePointer: %v", err)
	}

	owner, err := front.DomainOwner(context.Background(), edge.PreviewWildcard(previewBase))
	if err != nil {
		t.Fatalf("DomainOwner: %v", err)
	}
	if owner != edge.PreviewEntryOwner {
		t.Fatalf("the catch-all is owned by %q after a preview came down, want %q: it is a bootstrap item answering for every project this box serves, and taking it with one project's preview takes every other project's previews off the air",
			owner, edge.PreviewEntryOwner)
	}
	kept := front.SharedPreviewRemoval()
	if kept.Action != edge.PlanKeep || kept.Reason == "" {
		t.Errorf("the catch-all renders as %+v, want a kept row saying why it is kept", kept)
	}
}
