package caddyfile_test

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddyfile"
)

func TestYourCaddyRenewsTheCertificatesOfWhatOcelPlaces(t *testing.T) {
	t.Parallel()

	certificate, err := (caddyfile.Caddyfile{Box: &box{}}).Certificate(context.Background(), "shop.example.com")
	if err != nil || certificate.Renewal != caddyfile.Renewal || certificate.Trouble != nil {
		t.Errorf("Certificate() = %+v, %v; want renewal %q", certificate, err, caddyfile.Renewal)
	}
	if caddyfile.Renewal == certs.AdoptedRenewal {
		t.Errorf("renewal reads %q, the sentence for a proxy routed by hand", caddyfile.Renewal)
	}
}
