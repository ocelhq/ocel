package cli

import (
	"bytes"
	"context"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const (
	featureISR               = "isr"
	featureImageOptimization = "image-optimization"
	featureCloudflareEdge    = "cloudflare-edge"
)

func testCatalogue(enabled ...string) []*contractv1.Feature {
	on := func(name string) bool {
		for _, e := range enabled {
			if e == name {
				return true
			}
		}
		return false
	}
	return []*contractv1.Feature{
		{Name: featureISR, Summary: "incremental static regeneration", Enabled: on(featureISR)},
		{Name: featureImageOptimization, Summary: "on-demand image optimization", Enabled: on(featureImageOptimization)},
		{Name: featureCloudflareEdge, Summary: "a Cloudflare front", DependsOn: []string{featureISR}, Enabled: on(featureCloudflareEdge)},
	}
}

func TestParseFeatureFlag(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		raw  string
		want []string
	}{
		{"all takes everything the provider offers", "all", []string{featureISR, featureImageOptimization, featureCloudflareEdge}},
		{"none takes the core alone", "none", nil},
		{"a list keeps the provider's order", "cloudflare-edge,isr", []string{featureISR, featureCloudflareEdge}},
		{"space around a name is ignored", " isr , image-optimization ", []string{featureISR, featureImageOptimization}},
		{"a repeat collapses", "isr,isr", []string{featureISR}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := parseFeatureFlag(tc.raw, testCatalogue())
			if err != nil {
				t.Fatalf("parseFeatureFlag(%q): %v", tc.raw, err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("parseFeatureFlag(%q) = %v, want %v", tc.raw, got, tc.want)
			}
		})
	}

	t.Run("an unknown name names what there is", func(t *testing.T) {
		t.Parallel()

		_, err := parseFeatureFlag("quantum-edge", testCatalogue())
		if err == nil {
			t.Fatal("parseFeatureFlag accepted a feature the provider never offered")
		}
		for _, want := range []string{"quantum-edge", featureISR, "all", "none"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err, want)
			}
		}
	})

	t.Run("a set that leaves out a dependency names the whole one", func(t *testing.T) {
		t.Parallel()

		_, err := parseFeatureFlag(featureCloudflareEdge, testCatalogue())
		if err == nil {
			t.Fatal("parseFeatureFlag accepted a feature without what it stands on")
		}
		if !strings.Contains(err.Error(), "--features isr,cloudflare-edge") {
			t.Errorf("error %q does not name the full set to pass instead", err)
		}
	})
}

func TestDroppedFeatures(t *testing.T) {
	t.Parallel()

	got := droppedFeatures([]string{featureISR, featureImageOptimization}, []string{featureISR})
	if want := []string{featureImageOptimization}; !reflect.DeepEqual(got, want) {
		t.Errorf("droppedFeatures = %v, want %v", got, want)
	}
	if got := droppedFeatures([]string{featureISR}, []string{featureISR, featureCloudflareEdge}); got != nil {
		t.Errorf("droppedFeatures = %v, want nothing dropped when the set only grows", got)
	}
}

func TestWithDependencies(t *testing.T) {
	t.Parallel()

	got := withDependencies(testCatalogue(), []string{featureCloudflareEdge})
	if want := []string{featureISR, featureCloudflareEdge}; !reflect.DeepEqual(got, want) {
		t.Errorf("withDependencies = %v, want %v", got, want)
	}
}

func TestToggleFeatures(t *testing.T) {
	t.Parallel()

	catalogue := testCatalogue(featureISR)
	got, err := toggleFeatures(catalogue, []string{featureISR}, "1, 3")
	if err != nil {
		t.Fatalf("toggleFeatures: %v", err)
	}
	if want := []string{featureCloudflareEdge}; !reflect.DeepEqual(got, want) {
		t.Errorf("toggleFeatures = %v, want the first off and the third on", got)
	}
	if _, err := toggleFeatures(catalogue, nil, "9"); err == nil {
		t.Error("toggleFeatures accepted a number no feature answers to")
	}
}

func TestProjectFrameworks(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		cfg  *projectconfig.Config
		want []string
	}{
		{
			name: "a project with no apps names no framework",
			cfg:  &projectconfig.Config{},
		},
		{
			name: "an app with no framework is left out",
			cfg:  &projectconfig.Config{Apps: []projectconfig.App{{Name: "web"}}},
		},
		{
			name: "each app's framework is named",
			cfg:  &projectconfig.Config{Apps: []projectconfig.App{{Framework: "next"}, {Framework: "express"}}},
			want: []string{"express", "next"},
		},
		{
			name: "two apps on one framework name it once",
			cfg:  &projectconfig.Config{Apps: []projectconfig.App{{Framework: "next"}, {Framework: "next"}}},
			want: []string{"next"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got := projectFrameworks(tc.cfg)
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("projectFrameworks = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestBootstrapRefusesAFeatureSetAlongsideDestroy(t *testing.T) {
	t.Parallel()

	opts := bootstrapOptions{destroy: true, declared: true, features: "all"}
	err := runBootstrap(context.Background(), defaultDeps(), t.TempDir(), opts, &bytes.Buffer{}, &bytes.Buffer{}, strings.NewReader(""))
	if err == nil {
		t.Fatal("runBootstrap err = nil, want --features and --destroy refused together")
	}
	for _, want := range []string{"--features", "--destroy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("err = %v, want it to name %q", err, want)
		}
	}
}
