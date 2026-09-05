package bootstrap

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

func TestFeatureStackName(t *testing.T) {
	for _, tc := range []struct {
		class string
		want  string
	}{
		{ClassProduction, "ocel-bootstrap-isr"},
		{ClassPreview, "ocel-bootstrap-isr-preview"},
	} {
		f, ok := featureNamed(FeatureISR)
		if !ok {
			t.Fatalf("no %s feature in the registry", FeatureISR)
		}
		if got := f.stackName(tc.class); got != tc.want {
			t.Errorf("stackName(%q) = %q, want %q", tc.class, got, tc.want)
		}
	}
}

func TestEveryEdgeKindHasAFeatureOfItsOwn(t *testing.T) {
	for kind, want := range map[edge.Kind]string{
		KindCloudflare: FeatureCloudflareEdge,
		KindCloudFront: FeatureCloudFrontEdge,
		KindAPIGateway: FeatureAPIGatewayEdge,
	} {
		if got := providerkit.FeatureNeedingEdge(Catalogue(), kind); got != want {
			t.Errorf("FeatureNeedingEdge(%q) = %q, want %q: nothing else tells a run which stack the edge it fronts with stands in", kind, got, want)
		}
	}
}

func TestWhatThisCatalogueSaysAProjectNeeds(t *testing.T) {
	for _, tc := range []struct {
		name     string
		runtimes []string
		edge     string
		want     []string
	}{
		{
			name:     "a project on neither needs nothing",
			runtimes: []string{"node"},
		},
		{
			name:     "a Next app needs regeneration and image optimization",
			runtimes: []string{"node", "next"},
			want:     []string{FeatureISR, FeatureImageOptimization},
		},
		{
			name: "a Cloudflare front needs its feature and what it stands on",
			edge: "cloudflare",
			want: []string{FeatureISR, FeatureCloudflareEdge},
		},
		{
			name:     "a Cloudflare-fronted Next project needs all three, named once",
			runtimes: []string{"next"},
			edge:     "cloudflare",
			want:     []string{FeatureISR, FeatureImageOptimization, FeatureCloudflareEdge},
		},
		{
			name:     "a CloudFront-fronted project needs the CloudFront edge feature",
			runtimes: []string{"node"},
			edge:     "cloudfront",
			want:     []string{FeatureCloudFrontEdge},
		},
		{
			name:     "an API Gateway-fronted project needs the API Gateway edge feature",
			runtimes: []string{"node"},
			edge:     "api-gateway",
			want:     []string{FeatureAPIGatewayEdge},
		},
		{
			name:     "a CloudFront-fronted Next project needs its edge and what Next asks for",
			runtimes: []string{"next"},
			edge:     "cloudfront",
			want:     []string{FeatureISR, FeatureImageOptimization, FeatureCloudFrontEdge},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := providerkit.RequiredFeatures(Catalogue(), tc.runtimes, tc.edge)
			if err != nil {
				t.Fatalf("RequiredFeatures: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("RequiredFeatures(%v, %q) = %v, want %v", tc.runtimes, tc.edge, got, tc.want)
			}
		})
	}
}
