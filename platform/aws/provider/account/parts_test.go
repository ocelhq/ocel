package account

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
)

func TestAPartsStackIsNamedAfterThePartAndItsTier(t *testing.T) {
	for _, tc := range []struct {
		tier environment.Tier
		want string
	}{
		{environment.TierProduction, "ocel-bootstrap-cloudflare-edge"},
		{environment.TierPreview, "ocel-bootstrap-cloudflare-edge-preview"},
	} {
		f, ok := partNamed(CloudflareEdgePart)
		if !ok {
			t.Fatalf("no %s part in the registry", CloudflareEdgePart)
		}
		if got := f.stackName(defaultNamespace, tc.tier); got != tc.want {
			t.Errorf("stackName(%q) = %q, want %q", tc.tier, got, tc.want)
		}
	}
}

func TestOnlyTheCloudflareEdgeHasAPartOfItsOwn(t *testing.T) {
	for kind, want := range map[edge.Kind]string{
		KindCloudflare: CloudflareEdgePart,
		"cloudfront":   "",
		"api-gateway":  "",
	} {
		if got := bootstrapplan.PartFrontingEdge(Parts(), kind); got != want {
			t.Errorf("PartFrontingEdge(%q) = %q, want %q: an edge whose pieces a deploy adds on first use has no part", kind, got, want)
		}
	}
}

func TestOnlyACloudflareFrontedProjectNeedsAPartOnlyTheBootstrapCredentialAdds(t *testing.T) {
	for _, tc := range []struct {
		name string
		edge edge.Kind
		want []string
	}{
		{name: "a project with no edge needs no part only the bootstrap credential may add"},
		{
			name: "a Cloudflare front needs its part alone",
			edge: "cloudflare",
			want: []string{CloudflareEdgePart},
		},
		{
			name: "a CloudFront-fronted project needs no such part: a deploy adds the cloudfront part",
			edge: "cloudfront",
		},
		{
			name: "an API Gateway-fronted project needs no such part",
			edge: "api-gateway",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got := bootstrapplan.RequiredParts(Parts(), tc.edge)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("RequiredParts(%q) = %v, want %v", tc.edge, got, tc.want)
			}
		})
	}
}
