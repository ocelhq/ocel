package gcloud_test

import (
	"context"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func taggedIAM(t *testing.T, accounts ...string) (*gcloud.Clients, *googletest.IAM, *googletest.TierTags) {
	t.Helper()
	tags := googletest.NewTieredTags(t)
	fake := &googletest.IAM{Accounts: map[string]bool{}, TierTags: tags}
	for _, account := range accounts {
		fake.Accounts[account] = true
	}
	return tags.Tagged(fake.Open(t)), fake, tags
}

func TestAnAccountMadeForATierCarriesItsTiersTag(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clients, _, tags := taggedIAM(t)

	if err := clients.CreateAccount(ctx, environment.TierPreview, "ocel-app-1234567890", "shop web", "runs web"); err != nil {
		t.Fatal(err)
	}
	if got, want := tags.Bound(googletest.AccountTagParent("ocel-app-1234567890")), []string{tags.ValueOf(environment.TierPreview)}; !slices.Equal(got, want) {
		t.Errorf("the new account carries %v, want the preview value %v", got, want)
	}
}

func TestAnAccountFoundWithoutItsTiersTagIsGivenIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clients, fake, tags := taggedIAM(t, "ocel-app-1234567890")

	if err := clients.CreateAccount(ctx, environment.TierProduction, "ocel-app-1234567890", "shop web", "runs web"); err != nil {
		t.Fatal(err)
	}
	if len(fake.Created) != 0 {
		t.Errorf("an account that exists was created again: %d creates", len(fake.Created))
	}
	if got := tags.Bound(googletest.AccountTagParent("ocel-app-1234567890")); !slices.Equal(got, []string{tags.ValueOf(environment.TierProduction)}) {
		t.Errorf("the account found untagged carries %v, want the production value", got)
	}
}

func TestDeletingAnAccountRemovesItsTierTagBeforeTheAccount(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clients, fake, tags := taggedIAM(t, "ocel-app-1234567890")
	if err := clients.CreateAccount(ctx, environment.TierPreview, "ocel-app-1234567890", "shop web", "runs web"); err != nil {
		t.Fatal(err)
	}

	if _, err := clients.DeleteAccount(ctx, environment.TierPreview, "ocel-app-1234567890"); err != nil {
		t.Fatal(err)
	}
	if len(fake.DeletedAccounts) != 1 || len(fake.DeletedTagged) != 0 {
		t.Errorf("deleted %v, of which %v still carried a tag: Google keeps a deleted account's binding, which blocks deleting the tag value", fake.DeletedAccounts, fake.DeletedTagged)
	}
	if got := tags.Bound(googletest.AccountTagParent("ocel-app-1234567890")); len(got) != 0 {
		t.Errorf("the deleted account's binding %v is left behind", got)
	}
}
