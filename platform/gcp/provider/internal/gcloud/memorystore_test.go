package gcloud_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func TestAMemorystoreCallIsBilledToTheProjectItsClientsWereOpenedFor(t *testing.T) {
	googletest.UseUserCredential(t)
	var billed []string
	googletest.ServeAsGoogle(t, func(w http.ResponseWriter, r *http.Request) {
		billed = append(billed, r.Header.Get(googletest.QuotaProjectHeader))
		googletest.WriteBody(w, map[string]any{"name": r.URL.Path})
	})
	c := &gcloud.Clients{Names: googletest.Names, Region: googletest.Region}
	stores, err := c.Memorystore()
	if err != nil {
		t.Fatal(err)
	}

	if _, err := stores.ReadInstance(context.Background(), c.Location()+"/instances/cache"); err != nil {
		t.Fatal(err)
	}
	if _, _, err := stores.ListInstances(context.Background(), c.Location(), "", 10, ""); err != nil {
		t.Fatal(err)
	}

	if len(billed) != 2 {
		t.Fatalf("Memorystore was asked %d times, want 2", len(billed))
	}
	for _, project := range billed {
		if project != googletest.Project {
			t.Errorf("a Memorystore call was billed to %q, want %s: every call it makes works in the project its clients were opened for", project, googletest.Project)
		}
	}
}

func TestAMemorystoreCallOfACredentialGoogleNoLongerHonoursIsRefusedNamingHowToSignInAgain(t *testing.T) {
	for _, newAuthLibrary := range []string{"false", "true"} {
		t.Run("new auth library "+newAuthLibrary, func(t *testing.T) {
			t.Setenv("GOOGLE_API_GO_EXPERIMENTAL_ENABLE_NEW_AUTH_LIB", newAuthLibrary)
			googletest.UseUserCredential(t)
			asked := 0
			googletest.ServeAsGoogleRefusingTokens(t, func(w http.ResponseWriter, r *http.Request) {
				asked++
				googletest.WriteBody(w, map[string]any{"name": r.URL.Path})
			})
			c := &gcloud.Clients{Names: googletest.Names, Region: googletest.Region}
			stores, err := c.Memorystore()
			if err != nil {
				t.Fatal(err)
			}

			_, err = stores.ReadInstance(context.Background(), c.Location()+"/instances/cache")

			var refused refusal.Refusal
			if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied || !strings.Contains(refused.Message, "gcloud auth application-default login") {
				t.Errorf("ReadInstance() = %v, want a %s refusal naming `gcloud auth application-default login`: the credential's refresh token was refused", err, refusal.CodeDenied)
			}
			if asked != 0 {
				t.Errorf("Memorystore was asked %d times, want none: no token was got to ask it with", asked)
			}
		})
	}
}
