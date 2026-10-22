package edges

import (
	"testing"

	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
)

func TestAGoogleCloudOriginIsItsProject(t *testing.T) {
	t.Parallel()

	origin, err := CloudflareOrigin("acme-shop")(t.Context())
	if err != nil {
		t.Fatalf("CloudflareOrigin() = %v", err)
	}
	if want := (cloudflare.OriginScope{Vendor: "gcp", ID: "acme-shop"}); origin != want {
		t.Errorf("CloudflareOrigin() = %v, want %v: one bootstrap is one project", origin, want)
	}
}
