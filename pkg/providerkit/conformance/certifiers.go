package conformance

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type CertifierChecks struct {
	Kind edge.Kind

	Hostnames []string

	Handle func(hostname string) string
}

func runCertifier(t *testing.T, suite Suite) {
	t.Helper()

	construct := suite.New
	if construct == nil {
		construct = suite.Spec.New
	}
	if construct == nil {
		if suite.Certifier == nil {
			t.Skip("this suite carries no constructor and declares no certifier behaviour, so there is nothing here to hold anything to")
		}
		t.Fatal("the suite declares certifier behaviour and carries no constructor, so there is no provider to run it against")
	}
	provider, err := construct(context.Background(), suite.Options)
	if err != nil {
		t.Fatalf("New() error = %v, want a provider", err)
	}
	certifier, held := provider.(providerkit.Certifier)
	switch {
	case !held && suite.Certifier == nil:
		t.Skip("this provider implements no Certifier and the suite declares none, so there is nothing here to hold it to")
	case !held:
		t.Fatal("the suite declares certifier behaviour and the provider implements no Certifier, so `ocel domain status` blanks the certificate and renewal lines on it")
	case suite.Certifier == nil:
		t.Fatal("this provider implements Certifier and the suite states no checks for it, so the only tier that holds a certifier to anything skips over it. A provider opts out by certifying nothing, never by leaving the field off")
	}
	RunCertifier(t, certifier, *suite.Certifier)
}

func RunCertifier(t *testing.T, certifier providerkit.Certifier, checks CertifierChecks) {
	t.Helper()

	ctx := context.Background()

	t.Run("an empty handle is tolerated and never inspected as one", func(t *testing.T) {
		health, err := certifier.InspectCertificate(ctx, checks.Kind, "unbound.example.com", providerkit.Certificate{})
		if err != nil {
			t.Fatalf("InspectCertificate() of a binding naming no certificate = %v, want it tolerated: the edge conformance tier binds with an empty one", err)
		}
		if health.Issued {
			t.Error("a binding naming no certificate reports one issued, and nothing was ever asked for")
		}
	})

	t.Run("a held handle names what it terminates and who renews it", func(t *testing.T) {
		for _, hostname := range checks.Hostnames {
			cert := held(t, ctx, certifier, checks, hostname)
			health, err := certifier.InspectCertificate(ctx, checks.Kind, hostname, cert)
			if !health.Terminates {
				t.Errorf("InspectCertificate(%s, %s).Terminates = false (err = %v), and a zero health makes the kit skip every certificate case it reports on",
					hostname, cert.ID, err)
			}
			if health.Renewal == "" {
				t.Errorf("InspectCertificate(%s, %s) names nobody as the renewer (err = %v), and the whole reason a provider holds a Certifier is to say who is on the hook when it expires",
					hostname, cert.ID, err)
			}
		}
	})

	t.Run("a certificate ocel never requested is never ocel's to discard", func(t *testing.T) {
		for _, hostname := range checks.Hostnames {
			cert := held(t, ctx, certifier, checks, hostname)
			if cert.Requested {
				t.Errorf("Certificate(%s).Requested = true: Requested is a claim of delete authority and not a record of who did the work, and a provider that places no key material holds authority to remove none",
					hostname)
			}
			if err := certifier.DiscardCertificate(ctx, cert, edge.DiscardReporter()); err != nil {
				t.Errorf("DiscardCertificate(%s) = %v, want nil: the kit short-circuits on Requested, so this is unreachable and must not refuse if it is ever reached",
					cert.ID, err)
			}
		}
	})

	t.Run("the handle is the vocabulary this provider mints", func(t *testing.T) {
		if checks.Handle == nil {
			t.Skip("this provider states no handle for a hostname, so there is nothing to compare against")
		}
		for _, hostname := range checks.Hostnames {
			cert := held(t, ctx, certifier, checks, hostname)
			if want := checks.Handle(hostname); cert.ID != want {
				t.Errorf("Certificate(%s).ID = %q, want %q: nothing in the kit parses a handle, so it is the provider's own and must be the one it states",
					hostname, cert.ID, want)
			}
			if again := held(t, ctx, certifier, checks, hostname); again.ID != cert.ID {
				t.Errorf("Certificate(%s) minted %q and then %q, and a handle that moves under a hostname names a different slot on every status",
					hostname, cert.ID, again.ID)
			}
		}
	})

}

func held(t *testing.T, ctx context.Context, certifier providerkit.Certifier, checks CertifierChecks, hostname string) providerkit.Certificate {
	t.Helper()
	cert, err := certifier.Certificate(ctx, providerkit.CertificateRequest{
		Kind:     checks.Kind,
		Hostname: hostname,
		Report:   edge.DiscardReporter(),
		Prove: func(context.Context, providerkit.Certificate, []edge.Record) (providerkit.Certificate, error) {
			t.Errorf("Certificate(%s) asked for a validation record to be proved, and a provider that issues nothing proves nothing", hostname)
			return providerkit.Certificate{}, nil
		},
	})
	if err != nil {
		t.Fatalf("Certificate(%s) = %v", hostname, err)
	}
	if !cert.Held() {
		t.Fatalf("Certificate(%s) minted no handle, which renders as no certificate covering %s yet on a hostname that is served", hostname, hostname)
	}
	return cert
}
