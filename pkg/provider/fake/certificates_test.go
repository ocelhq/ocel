package fake_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
)

func TestACertificateReportedForAHostnameIsWhatInspectingThatHostnameReads(t *testing.T) {
	t.Parallel()

	p := fake.NewProvider(fake.Options{})
	p.ReportCertificate(provider.CertificateHealth{Renewal: "for every other hostname"})
	p.ReportCertificateFor("shop.example.com", provider.CertificateHealth{Renewal: "SUCCESS", ExpiresAt: 4102444800})

	for hostname, want := range map[string]string{"shop.example.com": "SUCCESS", "blog.example.com": "for every other hostname"} {
		health, err := p.Certificates().Inspect(context.Background(), fake.KindRelay, hostname, provider.Certificate{})
		if err != nil {
			t.Fatalf("Inspect(%s) err = %v", hostname, err)
		}
		if health.Renewal != want {
			t.Errorf("Inspect(%s) renewal = %q, want %q", hostname, health.Renewal, want)
		}
	}
}
