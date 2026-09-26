package conformance

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type CertificateChecks struct {
	Kind edge.Kind

	Hostnames []string

	Handle func(hostname string) string
}

func runCertificates(t *testing.T, suite Suite) {
	t.Helper()

	construct := suite.New
	if construct == nil {
		construct = suite.Server.New
	}
	if construct == nil {
		if suite.Certificates == nil {
			t.Skip("this suite has no constructor and declares no certificate checks, so there is nothing here to check")
		}
		t.Fatal("the suite declares certificate checks and has no constructor, so there is no provider to run them against")
	}
	p, err := construct(context.Background(), provider.Settings{Options: suite.Options})
	if err != nil {
		t.Fatalf("New() error = %v, want a provider", err)
	}
	if suite.Certificates == nil {
		t.Fatal("the suite states no checks for the provider's Certificates port, so the only tier that checks it skips over it. A provider opts out by certifying nothing, never by leaving the field off")
	}
	RunCertificates(t, p.Certificates(), *suite.Certificates)
}

func RunCertificates(t *testing.T, certificates provider.Certificates, checks CertificateChecks) {
	t.Helper()

	ctx := context.Background()

	t.Run("an empty handle is tolerated and never inspected as one", func(t *testing.T) {
		health, err := certificates.Inspect(ctx, checks.Kind, "unbound.example.com", provider.Certificate{})
		if err != nil {
			t.Fatalf("Inspect() of a binding naming no certificate = %v, want it tolerated: the edge conformance tier binds with an empty one", err)
		}
		if health.Issued {
			t.Error("a binding naming no certificate reports one issued, and nothing was ever asked for")
		}
	})

	t.Run("an issued handle names what it terminates and who renews it", func(t *testing.T) {
		for _, hostname := range certified(t, checks) {
			cert := issued(t, ctx, certificates, checks, hostname)
			health, err := certificates.Inspect(ctx, checks.Kind, hostname, cert)
			if !health.Terminates {
				t.Errorf("Inspect(%s, %s).Terminates = false (err = %v), and a zero health makes the kit skip every certificate case it reports on",
					hostname, cert.ID, err)
			}
			if health.Renewal == "" {
				t.Errorf("Inspect(%s, %s) names nobody as the renewer (err = %v), and the whole reason a provider implements a Certificates port is to say who is on the hook when it expires",
					hostname, cert.ID, err)
			}
		}
	})

	t.Run("a certificate ocel never requested is never ocel's to discard", func(t *testing.T) {
		for _, hostname := range certified(t, checks) {
			cert := issued(t, ctx, certificates, checks, hostname)
			if cert.Requested {
				t.Errorf("Issue(%s).Requested = true: Requested is a claim of delete authority and not a record of who did the work, and a provider that places no key material has authority to remove none",
					hostname)
			}
			if err := certificates.Discard(ctx, cert, edge.DiscardProgress()); err != nil {
				t.Errorf("Discard(%s) = %v, want nil: the kit short-circuits on Requested, so this is unreachable and must not refuse if it is ever reached",
					cert.ID, err)
			}
		}
	})

	t.Run("the handle is the vocabulary this provider mints", func(t *testing.T) {
		if checks.Handle == nil {
			t.Skip("this provider states no handle for a hostname, so there is nothing to compare against")
		}
		for _, hostname := range certified(t, checks) {
			cert := issued(t, ctx, certificates, checks, hostname)
			if want := checks.Handle(hostname); cert.ID != want {
				t.Errorf("Issue(%s).ID = %q, want %q: nothing in the kit parses a handle, so it is the provider's own and must be the one it states",
					hostname, cert.ID, want)
			}
			if again := issued(t, ctx, certificates, checks, hostname); again.ID != cert.ID {
				t.Errorf("Issue(%s) minted %q and then %q, and a handle that moves under a hostname names a different slot on every status",
					hostname, cert.ID, again.ID)
			}
		}
	})

}

func certified(t *testing.T, checks CertificateChecks) []string {
	t.Helper()
	if len(checks.Hostnames) == 0 {
		t.Skip("this suite names no hostname to mint a handle for, and a loop over none of them reports a pass having checked this provider against nothing")
	}
	return checks.Hostnames
}

func issued(t *testing.T, ctx context.Context, certificates provider.Certificates, checks CertificateChecks, hostname string) provider.Certificate {
	t.Helper()
	cert, err := certificates.Issue(ctx, provider.CertificateRequest{
		Kind:     checks.Kind,
		Hostname: hostname,
		Progress: edge.DiscardProgress(),
		Prove: func(context.Context, provider.Certificate, []edge.Record) (provider.Certificate, error) {
			t.Errorf("Issue(%s) asked for a validation record to be proved, and a provider that issues nothing proves nothing", hostname)
			return provider.Certificate{}, nil
		},
	})
	if err != nil {
		t.Fatalf("Issue(%s) = %v", hostname, err)
	}
	if !cert.Issued() {
		t.Fatalf("Issue(%s) minted no handle, which renders as no certificate covering %s yet on a hostname that is served", hostname, hostname)
	}
	return cert
}
