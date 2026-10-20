package gcloud_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
	"google.golang.org/api/secretmanager/v1"
)

func taggedSecrets(t *testing.T) (*gcloud.Clients, *secretmanager.Service, *googletest.Secrets, *googletest.TierTags) {
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
	service, err := clients.Secrets()
	if err != nil {
		t.Fatal(err)
	}
	return clients, service, secrets, tags
}

func TestASecretMadeForATierCarriesItsTiersTagInlineFromItsCreation(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clients, service, secrets, tags := taggedSecrets(t)

	if err := clients.EnsureSecret(ctx, service, environment.TierPreview, "ocel-preview-gate-shop"); err != nil {
		t.Fatal(err)
	}
	if got := secrets.InlineTags["ocel-preview-gate-shop"]; got["acme-prod/ocel-tier"] != "preview" {
		t.Errorf("the secret was created with tags %v, want acme-prod/ocel-tier: preview", got)
	}
	if len(tags.BindingsMade) != 0 {
		t.Errorf("a secret tagged at creation was bound again afterwards: %v", tags.BindingsMade)
	}
}

func TestASecretFoundWithoutItsTiersTagIsGivenIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clients, service, secrets, tags := taggedSecrets(t)
	secrets.WrittenFirstElsewhere = map[string]string{"ocel-preview-gate-shop": "held"}

	if err := clients.EnsureSecret(ctx, service, environment.TierPreview, "ocel-preview-gate-shop"); err != nil {
		t.Fatal(err)
	}
	if got := tags.Bound(googletest.SecretTagParent("ocel-preview-gate-shop")); !slices.Equal(got, []string{tags.ValueOf(environment.TierPreview)}) {
		t.Errorf("the secret found untagged carries %v, want the preview value", got)
	}
}

func TestDeletingASecretRemovesItsInlineTierTagBeforeTheSecret(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clients, service, secrets, tags := taggedSecrets(t)
	if err := clients.EnsureSecret(ctx, service, environment.TierPreview, "ocel-preview-gate-shop"); err != nil {
		t.Fatal(err)
	}

	for range 2 {
		if err := clients.DeleteSecret(ctx, service, environment.TierPreview, "ocel-preview-gate-shop"); err != nil {
			t.Fatal(err)
		}
	}
	if secrets.Has("ocel-preview-gate-shop") || len(secrets.DeletedTagged) != 0 {
		t.Errorf("the secret was deleted while it still carried %v: an inline tag outlives its secret and blocks deleting the tag value", secrets.DeletedTagged)
	}
	if got := tags.Bound(googletest.SecretTagParent("ocel-preview-gate-shop")); len(got) != 0 {
		t.Errorf("the deleted secret left the binding %v behind", got)
	}
}

const valueSecret = "ocel_values-shop-pr-7-web-stripe-key-abc123"

func TestALatestVersionIsAddedOnlyWhenThePayloadChangedAndEachWriteNamesTheVersionHoldingIt(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clients, service, secrets, _ := taggedSecrets(t)

	var written []string
	for _, payload := range []string{"one", "one", "two"} {
		version, err := clients.EnsureLatestVersion(ctx, service, environment.TierPreview, valueSecret, []byte(payload))
		if err != nil {
			t.Fatal(err)
		}
		written = append(written, version)
	}

	if !slices.Equal(written, []string{"1", "1", "2"}) {
		t.Errorf("the writes named versions %v, want 1, 1 and 2: an unchanged payload keeps the version a release pinned", written)
	}
	if secrets.VersionsAdded() != 2 {
		t.Errorf("%d versions were added, want 2", secrets.VersionsAdded())
	}
	if got := secrets.InlineTags[valueSecret]; got["acme-prod/ocel-tier"] != "preview" {
		t.Errorf("the secret was created with tags %v, want its tier's", got)
	}
}

func TestALatestVersionWrittenOverADestroyedOneIsANewVersion(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clients, service, secrets, _ := taggedSecrets(t)
	if _, err := clients.EnsureLatestVersion(ctx, service, environment.TierPreview, valueSecret, []byte("one")); err != nil {
		t.Fatal(err)
	}
	secrets.Destroy(valueSecret, 1)

	version, err := clients.EnsureLatestVersion(ctx, service, environment.TierPreview, valueSecret, []byte("one"))
	if err != nil {
		t.Fatal(err)
	}
	if version != "2" {
		t.Errorf("the write over a destroyed version named %s, want 2: no instance can read a destroyed version", version)
	}
}

func TestOnlyVersionsOlderThanTheNewestPinnedAndPinnedByNothingAreDestroyed(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	clients, service, secrets, _ := taggedSecrets(t)
	for _, payload := range []string{"one", "two", "three", "four", "five"} {
		if _, err := clients.EnsureLatestVersion(ctx, service, environment.TierPreview, valueSecret, []byte(payload)); err != nil {
			t.Fatal(err)
		}
	}

	if err := clients.DestroyUnpinnedVersions(ctx, service, valueSecret, []string{"2", "4"}); err != nil {
		t.Fatal(err)
	}
	if got := secrets.EnabledVersions(valueSecret); !slices.Equal(got, []int{2, 4, 5}) {
		t.Errorf("enabled versions = %v, want 2 and 4, which revisions pin, and 5, which is newer than any pin and may be a release's still being made", got)
	}
}
