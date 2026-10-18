package providerserver_test

import (
	"context"
	"errors"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/provider/ledger"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func TestDeployAnnouncesThePreviewHostnameOfTheProjectsOwnWildcard(t *testing.T) {
	builtProject(t)
	client, _ := deployServed(t)
	previewBootstrapped(t, client)

	result, _ := deploy(t, client, previewRequest())
	if !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}
	urls := servedURLs(result)
	if len(urls) != 1 || !regexp.MustCompile(`^https://pr-7-[a-z2-7]{24}\.preview\.example$`).MatchString(urls[0]) {
		t.Errorf("the preview deploy announced %v, want pr-7 and a signed token under the project's own wildcard", urls)
	}
}

func TestAPreviewKeepsItsAliasAndServesEachDeploymentOnAHostnameOfItsOwn(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)

	first, _ := deploy(t, client, previewRequest())
	second, _ := deploy(t, client, previewRequest())
	if !first.GetSuccess() || !second.GetSuccess() {
		t.Fatalf("Deploy() = %q, %q", first.GetError(), second.GetError())
	}
	if !slices.Equal(servedURLs(first), servedURLs(second)) {
		t.Errorf("the alias moved from %v to %v, want one alias for the life of the preview", servedURLs(first), servedURLs(second))
	}
	firstDeployment, secondDeployment := findDeploymentURL(first, "web"), findDeploymentURL(second, "web")
	if firstDeployment == "" || firstDeployment == secondDeployment || slices.Contains(servedURLs(first), firstDeployment) {
		t.Fatalf("deployment urls %q and %q beside alias %v, want each deployment a hostname of its own", firstDeployment, secondDeployment, servedURLs(first))
	}

	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)
	if pointer, builds := plane.FindServingPointer(strings.TrimPrefix(servedURLs(second)[0], "https://")); pointer != "pr-7" || builds["web"] == "" {
		t.Errorf("the alias is served by %q with %v, want the preview's pointer", pointer, builds)
	}
	want := router.FormatDeploymentPointer("pr-7", first.GetPromotionId())
	if pointer, builds := plane.FindServingPointer(strings.TrimPrefix(firstDeployment, "https://")); pointer != want || builds["web"] == "" {
		t.Errorf("the first deployment's hostname is served by %q with %v after the next deploy, want %s still serving it", pointer, builds, want)
	}
}

func TestPruneAndRemoveStopServingTheHostnamesOfWhatTheyDrop(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	first, _ := deploy(t, client, previewRequest())
	deploy(t, client, previewRequest())
	second, _ := deploy(t, client, previewRequest())
	if !first.GetSuccess() || !second.GetSuccess() {
		t.Fatalf("Deploy() = %q, %q", first.GetError(), second.GetError())
	}
	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)
	host := func(url string) string { return strings.TrimPrefix(url, "https://") }

	listed, err := client.ListEnvironments(context.Background(), &contractv1.ListEnvironmentsRequest{Slug: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if got := listed.GetEnvironments(); len(got) != 1 || !slices.Equal(got[0].GetAliasUrls(), servedURLs(second)) {
		t.Errorf("ListEnvironments = %v, want pr-7 listed with its alias %v", got, servedURLs(second))
	}

	pruning, err := client.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{
		Slug: "shop", KeepN: 1, Environment: previewRequest().GetEnvironment(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(pruning); err != nil || !result.GetSuccess() {
		t.Fatalf("RemoveStalePromotions() = %q, %v", result.GetError(), err)
	}
	if pointer, _ := plane.FindServingPointer(host(findDeploymentURL(first, "web"))); pointer != "" {
		t.Errorf("the pruned deployment's hostname is still served by %q, want nothing", pointer)
	}
	for _, kept := range []string{servedURLs(second)[0], findDeploymentURL(second, "web")} {
		if pointer, _ := plane.FindServingPointer(host(kept)); pointer == "" {
			t.Errorf("%s stopped serving, want what the prune kept still served", kept)
		}
	}

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
	if served := plane.ListServedHostnames(); len(served) != 0 {
		t.Errorf("after the preview was removed %v are still served, want none of its hostnames", served)
	}
}

func TestADeploymentHostnameAPruneFailedToWithdrawIsWithdrawnByRemove(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	first, _ := deploy(t, client, previewRequest())
	deploy(t, client, previewRequest())
	third, _ := deploy(t, client, previewRequest())
	if !first.GetSuccess() || !third.GetSuccess() {
		t.Fatalf("Deploy() = %q, %q", first.GetError(), third.GetError())
	}
	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)

	plane.FailNextPointerRemoval(errors.New("the data plane is down"))
	pruning, err := client.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{
		Slug: "shop", KeepN: 1, Environment: previewRequest().GetEnvironment(),
	})
	if err != nil {
		t.Fatal(err)
	}
	if result, err := drain(pruning); err == nil && result.GetSuccess() {
		t.Fatal("RemoveStalePromotions() succeeded through a refused pointer removal")
	}
	if pointer, _ := plane.FindServingPointer(strings.TrimPrefix(findDeploymentURL(first, "web"), "https://")); pointer == "" {
		t.Fatalf("the pruned deployment's hostname stopped serving through a refused removal, so this test has nothing left to withdraw")
	}

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
	if served := plane.ListServedHostnames(); len(served) != 0 {
		t.Errorf("after the preview was removed %v are still served, want the deployment hostname the prune failed to withdraw gone too", served)
	}
}

func TestAPruneThatFailedToWithdrawADeploymentHostnameReclaimsItsBuildOnTheRetry(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	first, _ := deploy(t, client, previewRequest())
	deploy(t, client, previewRequest())
	third, _ := deploy(t, client, previewRequest())
	if !first.GetSuccess() || !third.GetSuccess() {
		t.Fatalf("Deploy() = %q, %q", first.GetError(), third.GetError())
	}
	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)
	prune := func() []string {
		stream, err := client.RemoveStalePromotions(context.Background(), &contractv1.RemoveStalePromotionsRequest{
			Slug: "shop", KeepN: 1, Environment: previewRequest().GetEnvironment(),
		})
		if err != nil {
			t.Fatal(err)
		}
		var destroyed []string
		for _, event := range recorded(stream) {
			if line := saidLine(event); strings.HasPrefix(line, "Destroying the stack of web release") {
				destroyed = append(destroyed, line)
			}
		}
		return destroyed
	}

	plane.FailNextPointerRemoval(errors.New("the data plane is down"))
	if destroyed := prune(); len(destroyed) != 0 {
		t.Fatalf("the refused prune destroyed %v, want nothing destroyed while a hostname still routes to it", destroyed)
	}
	destroyed := prune()
	if pointer, _ := plane.FindServingPointer(strings.TrimPrefix(findDeploymentURL(first, "web"), "https://")); pointer != "" {
		t.Errorf("the retried prune left the pruned deployment's hostname served by %q, want nothing", pointer)
	}
	if len(destroyed) == 0 {
		t.Errorf("the retried prune destroyed nothing, want the releases of the deployments the refused prune dropped")
	}
}

func TestAnAliasTheNextDeployReplacesStopsServingAndRemoveStopsServingEveryAlias(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)
	host := func(url string) string { return strings.TrimPrefix(url, "https://") }

	first, _ := deploy(t, client, previewRequest())
	moved := previewRequest()
	moved.Manifest.Domains[0].Hostnames = []string{"*.moved.example"}
	second, _ := deploy(t, client, moved)
	if !first.GetSuccess() || !second.GetSuccess() {
		t.Fatalf("Deploy() = %q, %q", first.GetError(), second.GetError())
	}
	if slices.Equal(servedURLs(first), servedURLs(second)) {
		t.Fatalf("the alias stayed %v when domains.preview moved, want it on the new wildcard", servedURLs(first))
	}
	if pointer, _ := plane.FindServingPointer(host(servedURLs(first)[0])); pointer != "" {
		t.Errorf("the replaced alias %s is still served by %q, want nothing", servedURLs(first)[0], pointer)
	}
	if pointer, _ := plane.FindServingPointer(host(servedURLs(second)[0])); pointer != "pr-7" {
		t.Errorf("the new alias %s is served by %q, want pr-7", servedURLs(second)[0], pointer)
	}

	plane.FailNextPointerMove(errors.New("the data plane is down"))
	third, _ := deploy(t, client, previewRequest())
	if third.GetSuccess() {
		t.Fatal("Deploy() succeeded through a refused pointer move")
	}
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
	if served := plane.ListServedHostnames(); len(served) != 0 {
		t.Errorf("after the preview was removed %v are still served, want none of the aliases it ever published", served)
	}
}

func TestAPreviewWhoseAliasMoveFailsIsListedOnTheAliasStillServedAndRemoveWithdrawsBoth(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)

	first, _ := deploy(t, client, previewRequest())
	if !first.GetSuccess() {
		t.Fatalf("Deploy() = %q", first.GetError())
	}
	moved := previewRequest()
	moved.Manifest.Domains[0].Hostnames = []string{"*.moved.example"}
	plane.FailNextPointerMove(errors.New("the data plane is down"))
	if failed, _ := deploy(t, client, moved); failed.GetSuccess() {
		t.Fatal("Deploy() succeeded through a refused pointer move")
	}

	listed, err := client.ListEnvironments(context.Background(), &contractv1.ListEnvironmentsRequest{Slug: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if got := listed.GetEnvironments(); len(got) != 1 || !slices.Equal(got[0].GetAliasUrls(), servedURLs(first)) {
		t.Errorf("ListEnvironments = %v after the move to *.moved.example failed, want pr-7 listed on %v, the alias still served", got, servedURLs(first))
	}

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
	if served := plane.ListServedHostnames(); len(served) != 0 {
		t.Errorf("after the preview was removed %v are still served, want every alias it published or tried to", served)
	}
}

func TestAPreviewWhosePointerMoveFailsAfterItLandsKeepsItsLedgerAndAliasesOnThePromotionItReplaced(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)

	first, _ := deploy(t, client, previewRequest())
	if !first.GetSuccess() {
		t.Fatalf("Deploy() = %q", first.GetError())
	}
	moved := previewRequest()
	moved.Manifest.Domains[0].Hostnames = []string{"*.moved.example"}
	plane.FailNextPointerMoveAfterItLands(errors.New("the data plane timed out answering the write"))
	if failed, _ := deploy(t, client, moved); failed.GetSuccess() {
		t.Fatal("Deploy() succeeded through a failed pointer move")
	}

	active, err := ledger.New(vendor.KeyValues(), environment.TierPreview, "shop").ActivePromotionID(context.Background(), "pr-7")
	if err != nil || active != first.GetPromotionId() {
		t.Errorf("the ledger names %q, %v after the move failed, want %s, the promotion the failed deploy replaced", active, err, first.GetPromotionId())
	}
	listed, err := client.ListEnvironments(context.Background(), &contractv1.ListEnvironmentsRequest{Slug: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if got := listed.GetEnvironments(); len(got) != 1 || !slices.Equal(got[0].GetAliasUrls(), servedURLs(first)) {
		t.Errorf("ListEnvironments = %v after the move failed, want pr-7 listed on %v, the alias of the promotion the ledger names", got, servedURLs(first))
	}
	if pointer, _ := plane.FindServingPointer(strings.TrimPrefix(servedURLs(first)[0], "https://")); pointer != "pr-7" {
		t.Errorf("the alias %s is served by %q after the move failed, want pr-7", servedURLs(first)[0], pointer)
	}
}

func TestAPreviewWhosePromotionCannotBeTakenBackListsTheAliasesOfThePromotionTheLedgerStillNames(t *testing.T) {
	builtProject(t)
	client, vendor := deployServed(t)
	previewBootstrapped(t, client)
	plane := vendor.Routers().(*fake.Routers).DataPlane(fake.RouterRelay)
	store := vendor.KeyValues().(*fake.KeyValues)

	first, _ := deploy(t, client, previewRequest())
	if !first.GetSuccess() {
		t.Fatalf("Deploy() = %q", first.GetError())
	}
	pointer := ledger.Partition(environment.TierPreview, "shop").Key("pointers", "pr-7")
	var movesEveryWrite func()
	movesEveryWrite = func() {
		store.MoveBeforeNextWrite(pointer)
		store.BeforeNextWrite(pointer, movesEveryWrite)
	}
	moved := previewRequest()
	moved.Manifest.Domains[0].Hostnames = []string{"*.moved.example"}
	plane.BeforeNextPointerMove(func() { store.BeforeNextWrite(pointer, movesEveryWrite) })
	plane.FailNextPointerMoveAfterItLands(errors.New("the data plane timed out answering the write"))
	if failed, _ := deploy(t, client, moved); failed.GetSuccess() {
		t.Fatal("Deploy() succeeded through a failed pointer move")
	}

	active, err := ledger.New(vendor.KeyValues(), environment.TierPreview, "shop").ActivePromotionID(context.Background(), "pr-7")
	if err != nil || active == first.GetPromotionId() {
		t.Fatalf("the ledger names %q, %v, want the failed deploy's promotion still named: its take-back could not write", active, err)
	}
	var served []string
	for _, hostname := range plane.ListServedHostnames() {
		if at, _ := plane.FindServingPointer(hostname); at == "pr-7" {
			served = append(served, "https://"+hostname)
		}
	}
	listed, err := client.ListEnvironments(context.Background(), &contractv1.ListEnvironmentsRequest{Slug: "shop"})
	if err != nil {
		t.Fatal(err)
	}
	if got := listed.GetEnvironments(); len(got) != 1 || !slices.Equal(got[0].GetAliasUrls(), served) {
		t.Errorf("ListEnvironments = %v, want pr-7 listed on %v, the aliases of the promotion the ledger still names", got, served)
	}
}

func TestADeployRecordsEachStackItProvisionsAsWrittenByTheReleaseServingTheSession(t *testing.T) {
	builtProject(t)
	client, vendor := contractServed(t, "1.2.3")
	bootstrappedOverRPC(t, client)

	if result, _ := deploy(t, client, deployRequest()); !result.GetSuccess() {
		t.Fatalf("Deploy() = %q", result.GetError())
	}

	stacks, err := stackrecords.List(context.Background(), vendor.KeyValues(), environment.TierProduction, "shop")
	if err != nil {
		t.Fatal(err)
	}
	if len(stacks) == 0 {
		t.Fatal("the deploy recorded no stack, so the writer it recorded proves nothing")
	}
	for _, stack := range stacks {
		if stack.WrittenBy != "1.2.3" {
			t.Errorf("stack %s records writer %q, want 1.2.3, the release serving the session", stack.Name, stack.WrittenBy)
		}
	}
}
