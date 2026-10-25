package gcloud_test

import (
	"context"
	"net/http"
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func TestTheRegionCheckOfAUserCredentialIsBilledWhereTheCredentialSaysNotToTheProjectChecked(t *testing.T) {
	googletest.UseUserCredential(t)
	billed := []string{}
	googletest.ServeAsGoogle(t, func(w http.ResponseWriter, r *http.Request) {
		billed = append(billed, r.Header.Get(googletest.QuotaProjectHeader))
		googletest.WriteBody(w, map[string]any{"locations": []map[string]string{{"locationId": googletest.Region}}})
	})
	c := &gcloud.Clients{Names: googletest.Names, Region: googletest.Region}

	if err := c.RefuseRegionNotServed(context.Background()); err != nil {
		t.Fatalf("RefuseRegionNotServed() = %v, want %s admitted", err, googletest.Region)
	}
	if len(billed) != 1 || billed[0] != "elsewhere" {
		t.Errorf("the region check was billed to %q, want one call billed to elsewhere, the credential's own quota project: it runs before anything says %s has Cloud Run on, which billing a call to a project needs", billed, googletest.Project)
	}
}
