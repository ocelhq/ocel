package bootstrap

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/bootstrapplan"
)

func TestFeatureStackName(t *testing.T) {
	for _, tc := range []struct {
		tier environment.Tier
		want string
	}{
		{environment.TierProduction, "ocel-bootstrap-isr"},
		{environment.TierPreview, "ocel-bootstrap-isr-preview"},
	} {
		f, ok := featureNamed(FeatureISR)
		if !ok {
			t.Fatalf("no %s feature in the registry", FeatureISR)
		}
		if got := f.stackName(defaultNamespace, tc.tier); got != tc.want {
			t.Errorf("stackName(%q) = %q, want %q", tc.tier, got, tc.want)
		}
	}
}

func TestEveryEdgeKindHasAFeatureOfItsOwn(t *testing.T) {
	for kind, want := range map[edge.Kind]string{
		KindCloudflare: FeatureCloudflareEdge,
		KindCloudFront: FeatureCloudFrontEdge,
		KindAPIGateway: FeatureAPIGatewayEdge,
	} {
		if got := bootstrapplan.FeatureNeedingEdge(Catalogue(), kind); got != want {
			t.Errorf("FeatureNeedingEdge(%q) = %q, want %q: nothing else tells a run which stack the edge it fronts with is installed in", kind, got, want)
		}
	}
}

func TestWhatThisCatalogueSaysAProjectNeeds(t *testing.T) {
	next := buildoutput.Uses{ISR: true, ImageOptimization: true}
	for _, tc := range []struct {
		name string
		apps []buildoutput.Uses
		edge edge.Kind
		want []string
	}{
		{
			name: "a project whose builds use neither needs nothing",
			apps: []buildoutput.Uses{{}},
		},
		{
			name: "a build using ISR and image optimization needs both features",
			apps: []buildoutput.Uses{{}, next},
			want: []string{FeatureISR, FeatureImageOptimization},
		},
		{
			name: "a build using ISR alone needs no image optimizer",
			apps: []buildoutput.Uses{{ISR: true}},
			want: []string{FeatureISR},
		},
		{
			name: "a Cloudflare front needs its feature and what it depends on",
			edge: "cloudflare",
			want: []string{FeatureISR, FeatureCloudflareEdge},
		},
		{
			name: "a Cloudflare-fronted project using both needs all three, named once",
			apps: []buildoutput.Uses{next},
			edge: "cloudflare",
			want: []string{FeatureISR, FeatureImageOptimization, FeatureCloudflareEdge},
		},
		{
			name: "a CloudFront-fronted project needs the CloudFront edge feature",
			apps: []buildoutput.Uses{{}},
			edge: "cloudfront",
			want: []string{FeatureCloudFrontEdge},
		},
		{
			name: "an API Gateway-fronted project needs the API Gateway edge feature",
			apps: []buildoutput.Uses{{}},
			edge: "api-gateway",
			want: []string{FeatureAPIGatewayEdge},
		},
		{
			name: "a CloudFront-fronted project using both needs its edge and both features",
			apps: []buildoutput.Uses{next},
			edge: "cloudfront",
			want: []string{FeatureISR, FeatureImageOptimization, FeatureCloudFrontEdge},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := bootstrapplan.RequiredFeatures(Catalogue(), tc.apps, tc.edge)
			if err != nil {
				t.Fatalf("RequiredFeatures: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("RequiredFeatures(%+v, %q) = %v, want %v", tc.apps, tc.edge, got, tc.want)
			}
		})
	}
}
