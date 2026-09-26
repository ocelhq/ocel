package gcp_test

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider"
)

type reachedProject struct {
	err error

	asked string
}

func (r *reachedProject) Reaches(_ context.Context, project string) error {
	r.asked = project
	return r.err
}

func TestWhoamiNamesTheProviderTheIdentityCameFrom(t *testing.T) {
	t.Parallel()

	server := tokenInfo(t, `{"email":"deployer@acme.iam.gserviceaccount.com"}`)

	principal, err := gcp.Credentials{
		Project:      gcp.Named("acme-prod"),
		Region:       "europe-west1",
		Tokens:       fixedToken{token: mintedAccessToken},
		TokenInfoURL: server.URL,
		Projects:     &reachedProject{},
	}.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami() = %v, want an identity", err)
	}
	if principal.Vendor != gcp.Vendor {
		t.Errorf("Whoami().Provider = %q, want %q: the CLI renders a credential section per provider and drops the ones naming none", principal.Vendor, gcp.Vendor)
	}
}

func TestWhoamiAsksWhetherTheCredentialReachesTheProjectItWillDeployInto(t *testing.T) {
	t.Parallel()

	server := tokenInfo(t, `{"email":"deployer@acme.iam.gserviceaccount.com"}`)

	t.Run("a project the credential reaches", func(t *testing.T) {
		t.Parallel()

		reader := &reachedProject{}
		if _, err := (gcp.Credentials{
			Project:      gcp.Named("acme-prod"),
			Region:       "europe-west1",
			Tokens:       fixedToken{token: mintedAccessToken},
			TokenInfoURL: server.URL,
			Projects:     reader,
		}).Whoami(context.Background()); err != nil {
			t.Fatalf("Whoami() = %v, want an identity", err)
		}
		if reader.asked != "acme-prod" {
			t.Errorf("Whoami() asked about project %q, want the one the options name", reader.asked)
		}
	})

	t.Run("a project the credential cannot see", func(t *testing.T) {
		t.Parallel()

		denied := refusal.Refuse(refusal.CodeDenied, "this credential cannot see project acme-prod")
		var refused refusal.Refusal
		_, err := gcp.Credentials{
			Project:      gcp.Named("acme-prod"),
			Region:       "europe-west1",
			Tokens:       fixedToken{token: mintedAccessToken},
			TokenInfoURL: server.URL,
			Projects:     &reachedProject{err: denied},
		}.Whoami(context.Background())
		if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied {
			t.Fatalf("Whoami() = %v, want a denied refusal: a token that names a person still deploys nothing into a project it cannot read", err)
		}
	})
}

func TestAgainstTheEmulatorWhoamiSkipsGooglesTokenEndpointAndSaysWhereItIs(t *testing.T) {
	t.Parallel()

	endpoint := "http://127.0.0.1:4588"
	reader := &reachedProject{}
	principal, err := gcp.Credentials{
		Project:  gcp.Named("floci-local"),
		Region:   "europe-west1",
		Tokens:   fixedToken{err: errors.New("google: could not find default credentials")},
		Endpoint: endpoint,
		Projects: reader,
	}.Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami() against the emulator = %v, want an identity: the emulator mints no Google token to ask about", err)
	}
	if principal.Name != "emulator" {
		t.Errorf("Whoami().Principal = %q, want the emulator named as the principal", principal.Name)
	}
	if reader.asked != "floci-local" {
		t.Errorf("Whoami() asked about project %q, want the emulator's project still checked", reader.asked)
	}
	var said bool
	for _, detail := range principal.Details {
		said = said || strings.Contains(detail.Value, endpoint)
	}
	if !said {
		t.Errorf("Whoami().Details = %+v, want a row naming %q so nobody reads an emulator run as a run against Google", principal.Details, endpoint)
	}
}

func TestTheEmulatorEndpointIsReadOnceWhenTheProviderIsMade(t *testing.T) {
	endpoint := "http://127.0.0.1:4588"
	t.Setenv("OCEL_FLOCI_GCP_ENDPOINT", endpoint)

	p := newProvider(t, gcp.Options{Project: "floci-local", Region: "europe-west1"})
	t.Setenv("OCEL_FLOCI_GCP_ENDPOINT", "http://127.0.0.1:9999")

	credentials, own := p.Credentials().(gcp.Credentials)
	if !own {
		t.Fatalf("Credentials() = %T, want this provider's own", p.Credentials())
	}
	if credentials.Endpoint != endpoint {
		t.Errorf("the provider reaches %q, want %q: a run that reads the environment twice can address two clouds at once", credentials.Endpoint, endpoint)
	}
}

func TestWithoutTheEmulatorEveryClientAddressesGoogle(t *testing.T) {
	t.Setenv("OCEL_FLOCI_GCP_ENDPOINT", "")

	credentials, own := newProvider(t, gcp.Options{Project: "acme-prod", Region: "europe-west1"}).Credentials().(gcp.Credentials)
	if !own {
		t.Fatal("Credentials() is not this provider's own")
	}
	if credentials.Endpoint != "" {
		t.Errorf("the provider reaches %q, want Google's own endpoints", credentials.Endpoint)
	}
}

func TestAnEmulatorEndpointBeyondLoopbackIsRefusedRatherThanAddressedWithoutCredentials(t *testing.T) {
	for _, endpoint := range []string{"http://firestore.googleapis.com", "https://10.0.0.4:4588", "10.0.0.4:4588", "http://127.0.0.1.evil.example:4588"} {
		t.Run(endpoint, func(t *testing.T) {
			t.Setenv("OCEL_FLOCI_GCP_ENDPOINT", endpoint)

			var refused refusal.Refusal
			_, err := gcp.NewProvider(gcp.Options{Project: "acme-prod", Region: "europe-west1"})
			if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid {
				t.Fatalf("NewProvider() with %s naming %q = %v, want an %s refusal: every client at that endpoint is built with no authentication at all",
					"OCEL_FLOCI_GCP_ENDPOINT", endpoint, err, refusal.CodeInvalid)
			}
		})
	}
}

func TestAnEmulatorEndpointOnLoopbackIsAddressed(t *testing.T) {
	for _, endpoint := range []string{"http://127.0.0.1:4588", "localhost:4588", "http://[::1]:4588"} {
		t.Run(endpoint, func(t *testing.T) {
			t.Setenv("OCEL_FLOCI_GCP_ENDPOINT", endpoint)

			p, err := gcp.NewProvider(gcp.Options{Project: "floci-local", Region: "europe-west1"})
			if err != nil {
				t.Fatalf("NewProvider() against %q = %v, want the emulator addressed", endpoint, err)
			}
			credentials, own := p.Credentials().(gcp.Credentials)
			if !own || credentials.Endpoint != endpoint {
				t.Errorf("the provider reaches %q, want %q", credentials.Endpoint, endpoint)
			}
		})
	}
}
