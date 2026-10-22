package gcp

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	crm "google.golang.org/api/cloudresourcemanager/v3"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

var tierTagKey = bootstrap.Resource{Kind: bootstrap.KindTierTag, Name: names.TierTagKey, Shared: true}

func TestABootstrapBeginsWithTheProjectsTierTagKeyWhichBothTiersShare(t *testing.T) {
	t.Parallel()

	names := names.New("ocel", "acme-prod")
	for _, tier := range googletest.BothTiers {
		for _, emulated := range []bool{false, true} {
			items := bootstrap.Resources(names, tier, emulated)
			if items[0].ID() != tierTagKey.ID() || !items[0].Shared {
				t.Errorf("a %s bootstrap with emulated=%t begins with %s, want the shared %s every resource after it is bound to", tier, emulated, items[0].ID(), tierTagKey.ID())
			}
		}
	}
}

func TestRemovingATierKeepsTheTierTagKeyEveryOtherTierAndNamespaceBindsTo(t *testing.T) {
	t.Parallel()

	names := names.New("ocel", "acme-prod")
	read := bootstrap.Survey{Tier: environment.TierPreview, Names: names, Held: map[string]bool{tierTagKey.ID(): true}}
	for _, taking := range bootstrap.Removals(read) {
		if taking.Resource.ID() != tierTagKey.ID() {
			continue
		}
		if taking.Action != provider.ActionKeep || taking.Reason != bootstrap.ReasonTierTagKept {
			t.Errorf("removing the last tier of a namespace does %s to the tag key (%q), want it kept: %s", taking.Action, taking.Reason, bootstrap.ReasonTierTagKept)
		}
		return
	}
	t.Errorf("removing a tier plans nothing for %s", tierTagKey.ID())
}

func tierTagEngine(t *testing.T, tags *googletest.TierTags) (bootstrap.Engine, bootstrap.Survey) {
	t.Helper()
	clients := tags.Tagged(googletest.Clients(tags.URL))
	return bootstrap.Engine{Clients: clients}, bootstrap.Survey{Tier: environment.TierProduction, Names: clients.Names, Held: map[string]bool{}, Mending: map[string]string{}}
}

func TestABootstrapMakesTheTierTagKeyUnderTheProjectWithAValueForEachTier(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tags := googletest.NewTierTags(t)
	b, read := tierTagEngine(t, tags)

	if err := b.Provision(ctx, read, tierTagKey, nil); err != nil {
		t.Fatal(err)
	}
	key := tags.Keys["acme-prod/ocel-tier"]
	if key == nil || key.Parent != "projects/acme-prod" {
		t.Fatalf("the bootstrap made the keys %v, want acme-prod/ocel-tier under projects/acme-prod", tags.Keys)
	}
	for _, tier := range googletest.BothTiers {
		if tags.ValueOf(tier) == "" {
			t.Errorf("the tag key has no %s value, and that tier's resources would carry nothing", tier)
		}
	}
	found, err := b.PresenceOf(ctx, read, tierTagKey)
	if err != nil || !found.Present || found.Mends != "" {
		t.Errorf("the tag key just made reads %+v, %v, want present and current", found, err)
	}
}

func TestACredentialThatMayNotReadTheTierTagKeyIsToldWhatItLacksRatherThanShownAPlanToMakeIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	for _, denied := range []string{"resourcemanager.tagKeys.get", "resourcemanager.tagValues.get"} {
		tags := googletest.NewTieredTags(t)
		tags.Denied = []string{denied}
		b, read := tierTagEngine(t, tags)

		found, err := b.PresenceOf(ctx, read, tierTagKey)
		if err == nil || !strings.Contains(err.Error(), denied) {
			t.Errorf("a credential lacking %s reads the tag key as %+v, %v, want an error naming %s", denied, found, err, denied)
		}
	}
}

func TestATierTagKeyLackingATiersValueIsMendedWithIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	tags := googletest.NewTierTags(t)
	tags.Keys["acme-prod/ocel-tier"] = &crm.TagKey{Name: "tagKeys/7", Parent: "projects/123456789", ShortName: names.TierTagKey, NamespacedName: "acme-prod/ocel-tier"}
	b, read := tierTagEngine(t, tags)

	found, err := b.PresenceOf(ctx, read, tierTagKey)
	if err != nil || !found.Present || found.Mends == "" {
		t.Fatalf("a tag key with no values reads %+v, %v, want present and mended", found, err)
	}
	read.Held[tierTagKey.ID()], read.Mending[tierTagKey.ID()] = true, found.Mends
	if err := b.Provision(ctx, read, tierTagKey, nil); err != nil {
		t.Fatal(err)
	}
	if tags.KeysMade != 0 || tags.ValuesMade != 2 {
		t.Errorf("mending made %d keys and %d values, want no key and both tiers' values", tags.KeysMade, tags.ValuesMade)
	}
}

func TestATierBucketCarriesItsTiersTagOnceTheBootstrapMakesIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, read, server := bucketBootstrap(t)
	tags := googletest.NewTieredTags(t)
	tags.Tagged(b.Clients)
	target := tierBucketOf(naming.StorageState)

	if err := b.Provision(ctx, read, target, nil); err != nil {
		t.Fatal(err)
	}
	if len(server.created) != 1 {
		t.Fatalf("the bootstrap created %d buckets, want the state bucket", len(server.created))
	}
	parent := b.Clients.BucketTagged(server.created[0].Name)
	if got := tags.Bound(parent.Name); !slices.Equal(got, []string{tags.ValueOf(environment.TierProduction)}) {
		t.Errorf("the new state bucket carries %v, want the production value", got)
	}
}

func TestATierBucketFoundWithoutItsTiersTagIsMendedWithIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, read, _ := bucketBootstrap(t)
	target := tierBucketOf(naming.StorageEdge)
	if err := b.Provision(ctx, read, target, nil); err != nil {
		t.Fatal(err)
	}
	tags := googletest.NewTieredTags(t)
	tags.Tagged(b.Clients)
	recorded, err := b.Clients.TierBuckets(ctx, read.Tier, naming.StorageEdge)
	if err != nil {
		t.Fatal(err)
	}
	read.Buckets = recorded

	found, err := b.PresenceOf(ctx, read, target)
	if err != nil || !found.Present || found.Mends == "" {
		t.Fatalf("a tier bucket carrying no tag reads %+v, %v, want present and mended", found, err)
	}
	read.Held, read.Mending = map[string]bool{target.ID(): true}, map[string]string{target.ID(): found.Mends}
	if err := b.Provision(ctx, read, target, nil); err != nil {
		t.Fatal(err)
	}
	if got := tags.Bound(b.Clients.BucketTagged(recorded[naming.StorageEdge]).Name); len(got) != 1 {
		t.Errorf("the mended bucket carries %v, want the production value", got)
	}
}

func passphraseEngine(t *testing.T) (bootstrap.Engine, bootstrap.Survey, *googletest.Secrets, *googletest.TierTags) {
	t.Helper()
	tags := googletest.NewTieredTags(t)
	secrets := googletest.NewSecrets()
	secrets.TierTags = tags
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/v1/projects/"+googletest.Project {
			googletest.WriteBody(w, map[string]string{"projectId": googletest.Project, "projectNumber": googletest.ProjectNumber})
			return
		}
		secrets.Serve(t, w, r)
	}))
	t.Cleanup(server.Close)
	clients := tags.Tagged(googletest.Clients(server.URL))
	read := bootstrap.Survey{Tier: environment.TierPreview, Names: clients.Names, Held: map[string]bool{}}
	return bootstrap.Engine{Clients: clients}, read, secrets, tags
}

func TestTheTiersPassphraseSecretCarriesItsTiersTagFromItsCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, read, secrets, tags := passphraseEngine(t)
	target := bootstrap.ParameterResources(read.Names, read.Tier)[0]

	if err := b.Provision(ctx, read, target, nil); err != nil {
		t.Fatal(err)
	}
	if got := secrets.InlineTags[target.Name]; got["acme-prod/ocel-tier"] != "preview" {
		t.Errorf("the passphrase secret was created with tags %v, want acme-prod/ocel-tier: preview", got)
	}
	if got := tags.Bound(googletest.SecretTagParent(target.Name)); !slices.Equal(got, []string{tags.ValueOf(environment.TierPreview)}) {
		t.Errorf("the passphrase secret carries %v, want the preview value once", got)
	}
	if tags.BindingsAsked != 0 {
		t.Errorf("Resource Manager was asked for %d bindings, want none: the secret carried its tag from its creation", tags.BindingsAsked)
	}
}

func TestMendingTheTiersPassphraseSecretBindsItsTagAtMostOnce(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, read, _, tags := passphraseEngine(t)
	target := bootstrap.ParameterResources(read.Names, read.Tier)[0]
	if err := b.Provision(ctx, read, target, nil); err != nil {
		t.Fatal(err)
	}

	read.Held, read.Mending = map[string]bool{target.ID(): true}, map[string]string{target.ID(): "it was found wanting"}
	if err := b.Provision(ctx, read, target, nil); err != nil {
		t.Fatal(err)
	}
	if tags.BindingsAsked > 1 {
		t.Errorf("mending the passphrase secret asked Resource Manager for %d bindings, want at most one: the secret already exists", tags.BindingsAsked)
	}
}

func TestRemovingATiersPassphraseSecretRemovesItsTagBeforeTheSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	b, read, secrets, tags := passphraseEngine(t)
	target := bootstrap.ParameterResources(read.Names, read.Tier)[0]
	if err := b.Provision(ctx, read, target, nil); err != nil {
		t.Fatal(err)
	}

	if err := b.RemoveResource(ctx, read, bootstrap.Removal{Resource: target, Action: provider.ActionDelete}); err != nil {
		t.Fatal(err)
	}
	if secrets.Has(target.Name) || len(secrets.DeletedTagged) != 0 {
		t.Errorf("the passphrase secret was deleted while it carried %v: Google keeps an inline binding of a deleted secret, and the tag value can then never be deleted", secrets.DeletedTagged)
	}
	if got := tags.Bound(googletest.SecretTagParent(target.Name)); len(got) != 0 {
		t.Errorf("the removed passphrase secret left %v bound", got)
	}
}

func TestABootstrapAccountCarriesItsTiersTagBeforeItIsGrantedAnything(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	server := googletest.GrantedIAM()
	server.Accounts = map[string]bool{}
	c := server.Open(t)
	tags := googletest.NewTieredTags(t)
	tags.Tagged(c)
	const probe = "ocel-prod-probe"
	var boundWhenGranted []string
	b := bootstrap.Engine{Clients: c, Accounts: []bootstrap.Account{{
		Name: func(names.Names, environment.Tier) string { return probe },
		Purpose: func(bootstrap.Engine, environment.Tier) bootstrap.AccountPurpose {
			return bootstrap.AccountPurpose{Grant: func(context.Context, bootstrap.Engine, environment.Tier) error {
				boundWhenGranted = tags.Bound(googletest.AccountTagParent(probe))
				return nil
			}}
		},
	}}}

	if err := b.MakeAccount(ctx, bootstrap.Survey{Tier: environment.TierProduction, Names: c.Names}, probe); err != nil {
		t.Fatal(err)
	}
	if want := []string{tags.ValueOf(environment.TierProduction)}; !slices.Equal(boundWhenGranted, want) {
		t.Errorf("the account carried %v when its grants were made, want the production value already: an account granted before it is tagged can be bound to the other tier first", boundWhenGranted)
	}
}
