package bootstrap

import (
	"reflect"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
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

func TestWhatThisCatalogueSaysAProjectNeeds(t *testing.T) {
	for _, tc := range []struct {
		name       string
		frameworks []string
		edge       string
		want       []string
	}{
		{
			name:       "a project on neither needs nothing",
			frameworks: []string{"express"},
		},
		{
			name:       "a Next app needs regeneration and image optimization",
			frameworks: []string{"express", "next"},
			want:       []string{FeatureISR, FeatureImageOptimization},
		},
		{
			name: "a Cloudflare front needs its feature and what it stands on",
			edge: "cloudflare",
			want: []string{FeatureISR, FeatureCloudflareEdge},
		},
		{
			name:       "a Cloudflare-fronted Next project needs all three, named once",
			frameworks: []string{"next"},
			edge:       "cloudflare",
			want:       []string{FeatureISR, FeatureImageOptimization, FeatureCloudflareEdge},
		},
		{
			name:       "a CloudFront-fronted project needs no edge feature",
			frameworks: []string{"express"},
			edge:       "cloudfront",
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			got, err := providerkit.RequiredFeatures(Catalogue(), tc.frameworks, tc.edge)
			if err != nil {
				t.Fatalf("RequiredFeatures: %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("RequiredFeatures(%v, %q) = %v, want %v", tc.frameworks, tc.edge, got, tc.want)
			}
		})
	}
}
