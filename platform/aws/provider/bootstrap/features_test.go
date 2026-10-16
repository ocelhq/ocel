package bootstrap

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
)

func TestFeatureStackName(t *testing.T) {
	for _, tc := range []struct {
		tier environment.Tier
		want string
	}{
		{environment.TierProduction, "ocel-bootstrap-cloudflare-edge"},
		{environment.TierPreview, "ocel-bootstrap-cloudflare-edge-preview"},
	} {
		f, ok := featureNamed(FeatureCloudflareEdge)
		if !ok {
			t.Fatalf("no %s feature in the registry", FeatureCloudflareEdge)
		}
		if got := f.stackName(defaultNamespace, tc.tier); got != tc.want {
			t.Errorf("stackName(%q) = %q, want %q", tc.tier, got, tc.want)
		}
	}
}

func TestOnlyTheCloudflareEdgeHasAFeatureOfItsOwn(t *testing.T) {
	for kind, want := range map[edge.Kind]string{
		KindCloudflare: FeatureCloudflareEdge,
		"cloudfront":   "",
		"api-gateway":  "",
	} {
		if got := bootstrapplan.FeatureNeedingEdge(Catalogue(), kind); got != want {
			t.Errorf("FeatureNeedingEdge(%q) = %q, want %q: an edge whose pieces a deploy adds on first use has no feature", kind, got, want)
		}
	}
}

func TestWhatThisCatalogueSaysAProjectNeeds(t *testing.T) {
	for _, tc := range []struct {
		name string
		edge edge.Kind
		want []string
	}{
		{name: "a project with no edge needs no part only the bootstrap credential may add"},
		{
			name: "a Cloudflare front needs its part alone",
			edge: "cloudflare",
			want: []string{FeatureCloudflareEdge},
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
			got, err := bootstrapplan.RequiredFeatures(Catalogue(), tc.edge)
			if err != nil {
				t.Fatalf("RequiredFeatures: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("RequiredFeatures(%q) = %v, want %v", tc.edge, got, tc.want)
			}
		})
	}
}
