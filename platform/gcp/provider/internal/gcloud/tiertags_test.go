package gcloud_test

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func taggedClients(t *testing.T) (*gcloud.Clients, *googletest.TierTags) {
	t.Helper()
	tags := googletest.NewTieredTags(t)
	return tags.Tagged(googletest.Clients("http://127.0.0.1:1")), tags
}

func TestATierTagBindsAResourceToItsTiersValueOnceHoweverOftenItIsEnsured(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clients, tags := taggedClients(t)
	service := clients.RunServiceTagged("shop-web")

	for range 2 {
		if err := clients.EnsureTierTag(ctx, environment.TierPreview, service); err != nil {
			t.Fatal(err)
		}
	}
	if got, want := tags.Bound(service.Name), []string{tags.ValueOf(environment.TierPreview)}; !slices.Equal(got, want) {
		t.Errorf("%s carries %v, want only the preview value %v", service.Name, got, want)
	}
	if tags.ValueLookups != 1 {
		t.Errorf("the preview value was looked up %d times, want once: its id never changes", tags.ValueLookups)
	}
}

func TestAResourceCarryingTheOtherTiersTagIsRefusedRatherThanRetagged(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clients, tags := taggedClients(t)
	topic := clients.PubSubTagged("projects/acme-prod/topics/shop-orders")
	if err := clients.EnsureTierTag(ctx, environment.TierProduction, topic); err != nil {
		t.Fatal(err)
	}

	err := clients.EnsureTierTag(ctx, environment.TierPreview, topic)
	if err == nil || !strings.Contains(err.Error(), "acme-prod/ocel-tier/production") {
		t.Fatalf("binding preview onto a production topic answered %v, want a refusal naming the production value it carries", err)
	}
	if got := tags.Bound(topic.Name); !slices.Equal(got, []string{tags.ValueOf(environment.TierProduction)}) {
		t.Errorf("the production topic carries %v after the refusal, want its production value alone", got)
	}
}

func TestABindingAskedBeforeANewResourceIsVisibleIsAskedAgain(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clients, tags := taggedClients(t)
	tags.BindingsUnseen = 2
	account := clients.AccountTagged("104857600000000000001")

	if err := clients.EnsureTierTag(ctx, environment.TierProduction, account); err != nil {
		t.Fatal(err)
	}
	if got := tags.Bound(account.Name); len(got) != 1 {
		t.Errorf("the new account carries %v, want the production value once Resource Manager sees it", got)
	}
}

func TestRemovingATierTagLeavesNoBindingAndRemovingItAgainAsksNothingOfDeletion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clients, tags := taggedClients(t)
	database := clients.DatabaseTagged("ocel-shop-production-values")
	if err := clients.EnsureTierTag(ctx, environment.TierProduction, database); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := clients.RemoveTierTag(ctx, environment.TierProduction, database); err != nil {
			t.Fatal(err)
		}
	}
	if got := tags.Bound(database.Name); len(got) != 0 {
		t.Errorf("%s still carries %v after its tier tag was removed", database.Name, got)
	}
	if len(tags.BindingsRemoved) != 1 {
		t.Errorf("%d bindings were deleted, want the one the database carried", len(tags.BindingsRemoved))
	}
}

func TestAProjectWithoutTheTierTagValueIsToldToBootstrapTheTier(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tags := googletest.NewTierTags(t)
	clients := tags.Tagged(googletest.Clients("http://127.0.0.1:1"))

	err := clients.EnsureTierTag(ctx, environment.TierPreview, clients.BucketTagged("ocel-preview-state"))
	if err == nil || !strings.Contains(err.Error(), "ocel bootstrap preview") {
		t.Fatalf("binding a tag value the project lacks answered %v, want it to name the bootstrap that makes it", err)
	}
}

func TestARegionalResourceIsBoundThroughItsRegionsEndpointAndAGlobalOneThroughTheGlobalOne(t *testing.T) {
	t.Parallel()
	clients := googletest.Clients("")

	regional := clients.RunServiceTagged("shop-web")
	if got, want := clients.TagEndpointOf(regional), "https://europe-west1-cloudresourcemanager.googleapis.com/"; got != want {
		t.Errorf("a Cloud Run service binding in %s goes to %s, want %s", googletest.Region, got, want)
	}
	global := clients.AccountTagged("104857600000000000001")
	if got, want := clients.TagEndpointOf(global), "https://cloudresourcemanager.googleapis.com/"; got != want {
		t.Errorf("a service account binding goes to %s, want %s", got, want)
	}
}

func TestAnEmulatorThatServesNoTagsLeavesResourcesUntaggedWithoutAsking(t *testing.T) {
	t.Parallel()
	clients := googletest.Clients("http://127.0.0.1:1")

	if err := clients.EnsureTierTag(context.Background(), environment.TierPreview, clients.PubSubTagged("projects/acme-prod/topics/shop-orders")); err != nil {
		t.Errorf("binding against an emulator that serves no tags answered %v, want nothing asked of it", err)
	}
	if tags := clients.InlineTierTags(environment.TierPreview); tags != nil {
		t.Errorf("a resource made against the emulator would carry %v inline, and the emulator serves no tags", tags)
	}
}

func TestAResourceMadeOnGoogleCarriesItsTiersTagInlineByTheProjectNamespacedKey(t *testing.T) {
	t.Parallel()
	clients := googletest.Clients("")

	got := clients.InlineTierTags(environment.TierProduction)
	if len(got) != 1 || got["acme-prod/ocel-tier"] != "production" {
		t.Errorf("an inline tier tag reads %v, want acme-prod/ocel-tier: production", got)
	}
}

func TestABindingTheCredentialMayNotMakeNamesBothTheValueAndTheResourcePermissionItNeeds(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clients, tags := taggedClients(t)
	tags.BindingsDenied = true
	repository := clients.RepositoryTagged("ocel-acme-prod-preview-gate")

	err := clients.EnsureTierTag(ctx, environment.TierPreview, repository)
	for _, want := range []string{
		"roles/resourcemanager.tagUser on acme-prod/ocel-tier/preview",
		"artifactregistry.repositories.createTagBinding on " + repository.Name,
	} {
		if err == nil || !strings.Contains(err.Error(), want) {
			t.Errorf("a binding Google refused answered %v, want it to name %q", err, want)
		}
	}
}

func TestUntaggingAgainstAnEmulatorThatServesNoTagsLooksForNothing(t *testing.T) {
	t.Parallel()
	clients := googletest.Clients("http://127.0.0.1:1")

	err := clients.RemoveTierTagIfFound(context.Background(), environment.TierPreview, func(context.Context) (gcloud.TaggedResource, bool, error) {
		t.Errorf("the resource was looked for against an emulator that serves no tags")
		return gcloud.TaggedResource{}, false, nil
	})
	if err != nil {
		t.Errorf("untagging against an emulator that serves no tags answered %v, want nothing asked of it", err)
	}
}

func TestABindingListedUnderTheProjectNumberIsKnownByItsValue(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clients, tags := taggedClients(t)
	topic := clients.PubSubTagged("projects/acme-prod/topics/shop-orders")
	if err := clients.EnsureTierTag(ctx, environment.TierProduction, topic); err != nil {
		t.Fatal(err)
	}
	tags.Mu.Lock()
	tags.NumberedNames = true
	tags.Mu.Unlock()

	bound, err := clients.HasTierTag(ctx, environment.TierProduction, topic)
	if err != nil || !bound {
		t.Errorf("HasTierTag() over a binding named 123456789/ocel-tier/production = %v, %v, want true: Google may name a value by the project number", bound, err)
	}
}
