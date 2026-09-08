package gcp_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

const heldAccessToken = "ya29.a0-secret"

type heldToken struct {
	token string
	err   error
}

func (h heldToken) Token(context.Context) (string, error) { return h.token, h.err }

func tokenInfo(t *testing.T, body string) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+heldAccessToken {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(body))
	}))
	t.Cleanup(server.Close)
	return server
}

func unreachable(t *testing.T) string {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	server.Close()
	return server.URL
}

func TestWhoamiNamesTheProjectTheRegionAndWhoTheTokenBelongsTo(t *testing.T) {
	t.Parallel()

	server := tokenInfo(t, `{"email":"deployer@acme.iam.gserviceaccount.com","email_verified":"true"}`)

	identity, err := gcp.Credentials{
		Project:      "acme-prod",
		Region:       "europe-west1",
		Tokens:       heldToken{token: heldAccessToken},
		TokenInfoURL: server.URL,
		Projects:     &reachedProject{},
	}.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami() = %v, want the identity the ADC token belongs to", err)
	}
	if named := providerkit.IdentityProto(gcp.Vendor, identity).GetProvider(); named != string(gcp.Vendor) {
		t.Errorf("the identity names %q and the provider names itself %q; the CLI matches a credential problem to its section by that string, so a mismatch loses the problem", named, gcp.Vendor)
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
		{
			name: "nothing listening where the token endpoint should be",
			held: gcp.Credentials{
				Project:      "acme-prod",
				Tokens:       heldToken{token: heldAccessToken},
				TokenInfoURL: unreachable(t),
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
			if strings.Contains(refusal.Message, "GOOGLE_APPLICATION_CREDENTIALS") {
				t.Errorf("Whoami() refused with %q, and a key file is not a way this provider authenticates", refusal.Message)
			}
		})
	}
}

func TestTheAccessTokenReachesNoUrlAndNoErrorString(t *testing.T) {
	t.Parallel()

	var asked atomic.Value
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		asked.Store(r.URL.String())
		w.WriteHeader(http.StatusUnauthorized)
	}))
	t.Cleanup(server.Close)

	for name, endpoint := range map[string]string{
		"a token the endpoint rejects":   server.URL,
		"an endpoint nothing answers on": unreachable(t),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			_, err := gcp.Credentials{
				Project:      "acme-prod",
				Tokens:       heldToken{token: heldAccessToken},
				TokenInfoURL: endpoint,
			}.Whoami(context.Background())
			if err == nil {
				t.Fatal("Whoami() answered, want a refusal")
			}
			if strings.Contains(err.Error(), heldAccessToken) {
				t.Errorf("Whoami() failed with %q, which carries the access token into every log line and CLI output that prints it", err)
			}
		})
	}

	if url, _ := asked.Load().(string); strings.Contains(url, heldAccessToken) {
		t.Errorf("the token endpoint was asked at %q, which leaves the access token in every proxy and access log on the way", url)
	}
}

func TestAThrottledTokenEndpointIsRetriedAndThenSaidToBeBusy(t *testing.T) {
	t.Parallel()

	t.Run("a throttle that passes is waited out", func(t *testing.T) {
		t.Parallel()

		var asked atomic.Int32
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			if asked.Add(1) < 3 {
				w.WriteHeader(http.StatusTooManyRequests)
				return
			}
			_, _ = w.Write([]byte(`{"email":"deployer@acme.iam.gserviceaccount.com"}`))
		}))
		t.Cleanup(server.Close)

		identity, err := gcp.Credentials{
			Project:      "acme-prod",
			Tokens:       heldToken{token: heldAccessToken},
			TokenInfoURL: server.URL,
			Projects:     &reachedProject{},
		}.Whoami(context.Background())
		if err != nil {
			t.Fatalf("Whoami() = %v, want a throttle waited out rather than read as a dead credential", err)
		}
		if identity.Principal != "deployer@acme.iam.gserviceaccount.com" {
			t.Errorf("Whoami().Principal = %q, want the email the attempt that got through answered with", identity.Principal)
		}
	})

	for name, status := range map[string]int{
		"throttled throughout": http.StatusTooManyRequests,
		"broken throughout":    http.StatusInternalServerError,
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			var asked atomic.Int32
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				asked.Add(1)
				w.WriteHeader(status)
			}))
			t.Cleanup(server.Close)

			var refusal providerkit.Refusal
			_, err := gcp.Credentials{
				Project:      "acme-prod",
				Tokens:       heldToken{token: heldAccessToken},
				TokenInfoURL: server.URL,
			}.Whoami(context.Background())
			if !errors.As(err, &refusal) || refusal.Code != providerkit.CodeBusy {
				t.Fatalf("Whoami() = %v, want a %s refusal: the credential is good, the endpoint is not", err, providerkit.CodeBusy)
			}
			if got := asked.Load(); got < 2 {
				t.Errorf("the token endpoint was asked %d time(s), want a bounded retry before the refusal", got)
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
