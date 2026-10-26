package gcloud_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func TestACloudStorageCallOfAUserCredentialIsBilledToTheProjectItsClientsWereOpenedFor(t *testing.T) {
	googletest.UseUserCredential(t)
	var billed []string
	googletest.ServeAsGoogle(t, func(w http.ResponseWriter, r *http.Request) {
		billed = append(billed, r.Header.Get(googletest.QuotaProjectHeader))
		googletest.WriteBody(w, map[string]any{"name": "acme-prod-state"})
	})
	c := &gcloud.Clients{Names: googletest.Names, Region: googletest.Region}
	client, err := c.Storage()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := client.Bucket("acme-prod-state").Attrs(context.Background()); err != nil {
		t.Fatal(err)
	}

	if len(billed) != 1 || billed[0] != googletest.Project {
		t.Errorf("Cloud Storage calls were billed to %q, want one billed to %s: a user's credential bills the project it deploys to", billed, googletest.Project)
	}
}
