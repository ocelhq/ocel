package gcp_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

type heldToken struct {
	token string
	err   error
}

func (h heldToken) Token(context.Context) (string, error) { return h.token, h.err }

func tokenInfo(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("access_token") != "ya29.a0" {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func TestWhoamiNamesTheProjectTheRegionAndWhoTheTokenBelongsTo(t *testing.T) {
	t.Parallel()

	server := tokenInfo(t, `{"email":"deployer@acme.iam.gserviceaccount.com","email_verified":"true"}`)

	identity, err := gcp.Credentials{
		Project:      "acme-prod",
		Region:       "europe-west1",
		Tokens:       heldToken{token: "ya29.a0"},
		TokenInfoURL: server.URL,
	}.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami() = %v, want the identity the ADC token belongs to", err)
	}
	if identity.Provider != gcp.Vendor {
		t.Errorf("Whoami().Provider = %q, want %q", identity.Provider, gcp.Vendor)
	}
	if identity.Account != "acme-prod" {
		t.Errorf("Whoami().Account = %q, want the project the options name", identity.Account)
	}
	if identity.Location != "europe-west1" {
		t.Errorf("Whoami().Location = %q, want the region this run acts in", identity.Location)
	}
	if identity.Principal != "deployer@acme.iam.gserviceaccount.com" {
		t.Errorf("Whoami().Principal = %q, want the email the token belongs to", identity.Principal)
	}
}

func TestWhoamiRefusesWhenThereAreNoApplicationDefaultCredentials(t *testing.T) {
	t.Parallel()

	server := tokenInfo(t, `{}`)

	for _, tc := range []struct {
		name string
		held gcp.Credentials
	}{
		{
			name: "no credentials to mint a token from",
			held: gcp.Credentials{
				Project:      "acme-prod",
				Tokens:       heldToken{err: errors.New("google: could not find default credentials")},
				TokenInfoURL: server.URL,
			},
		},
		{
			name: "a token nothing will vouch for",
			held: gcp.Credentials{
				Project:      "acme-prod",
				Tokens:       heldToken{token: "expired"},
				TokenInfoURL: server.URL,
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var refusal providerkit.Refusal
			identity, err := tc.held.Whoami(context.Background())
			if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeDenied {
				t.Fatalf("Whoami() = %+v, %v, want a denied refusal", identity, err)
			}
			if !strings.Contains(refusal.Message, "gcloud auth application-default login") {
				t.Errorf("Whoami() refused with %q, want it to name the command that fixes it", refusal.Message)
			}
		})
	}
}

func TestPermissionsRefuseTheTierThatIsNoTier(t *testing.T) {
	t.Parallel()

	var refusal providerkit.Refusal
	if _, err := (gcp.Credentials{}).Permissions("neither"); !errors.As(err, &refusal) || refusal.Code != providerkit.CodeInvalid {
		t.Fatalf("Permissions(neither) = %v, want an invalid refusal", err)
	}
}
