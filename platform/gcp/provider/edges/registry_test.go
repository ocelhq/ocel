package edges

import (
	"testing"

	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func TestAGoogleCloudOriginIsItsProject(t *testing.T) {
	t.Parallel()

	origin, err := Registry{Project: "acme-shop"}.Cloudflare().Scope(t.Context())
	if err != nil {
		t.Fatalf("Cloudflare().Scope() = %v", err)
	}
	if want := (cloudflare.OriginScope{Vendor: "gcp", ID: "acme-shop"}); origin != want {
		t.Errorf("Cloudflare().Scope() = %v, want %v: one bootstrap is one project", origin, want)
	}
}
