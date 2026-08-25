package bootstrap

import (
	"context"
	"reflect"
	"strings"
	"testing"

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

func TestParseRemoveFlag(t *testing.T) {
	t.Parallel()

	got, err := parseRemoveFlag(" cloudflare-edge , isr ", testCatalogue())
	if err != nil {
		t.Fatalf("parseRemoveFlag: %v", err)
	}
	if want := []string{featureISR, featureCloudflareEdge}; !reflect.DeepEqual(got, want) {
		t.Errorf("parseRemoveFlag = %v, want %v", got, want)
	}
	if _, err := parseRemoveFlag("quantum-edge", testCatalogue()); err == nil {
		t.Error("parseRemoveFlag accepted a feature the provider never offered")
	}
}

func TestGoingFeatures(t *testing.T) {
	t.Parallel()

	standing := []string{featureISR, featureImageOptimization, featureCloudflareEdge}
	got := goingFeatures(testCatalogue(), standing, []string{featureISR})
	if want := []string{featureISR, featureCloudflareEdge}; !reflect.DeepEqual(got, want) {
		t.Errorf("goingFeatures = %v, want what stands on the named feature to go with it", got)
	}
	if got := goingFeatures(testCatalogue(), []string{featureISR}, []string{featureImageOptimization}); got != nil {
		t.Errorf("goingFeatures = %v, want a name that is not standing to be nothing to remove", got)
	}
}

func TestBothWays(t *testing.T) {
	t.Parallel()

	if err := bothWays([]string{featureISR}, []string{featureImageOptimization}); err != nil {
		t.Errorf("bothWays = %v, want two disjoint sets admitted", err)
	}
	err := bothWays([]string{featureISR}, []string{featureISR})
	if err == nil {
		t.Fatal("bothWays admitted a feature named for both ensure and removal")
	}
	for _, want := range []string{"--features", "--remove", featureISR} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not name %q", err, want)
		}
	}
}

func TestThePickerOnlyEverOffersToAdd(t *testing.T) {
	t.Parallel()

	catalogue := testCatalogue(featureISR)
	got := addableFeatures(catalogue, []string{featureISR})
	if want := []string{featureImageOptimization, featureCloudflareEdge}; !reflect.DeepEqual(got, want) {
		t.Errorf("addableFeatures = %v, want %v — a standing feature is shown, never offered as a toggle", got, want)
	}

	var out strings.Builder
	printStanding(&out, []string{featureISR})
	if !strings.Contains(out.String(), "✓ "+featureISR) {
		t.Errorf("printStanding = %q, want the standing feature marked as kept", out.String())
	}

	var skipped strings.Builder
	names := []string{featureISR, featureImageOptimization, featureCloudflareEdge}
	applied, selected, err := pickFeatures(context.Background(), testCatalogue(names...), names, nil, &skipped)
	if err != nil || !selected {
		t.Fatalf("pickFeatures = %v, %v, want an account with nothing left to add to skip the prompt", selected, err)
	}
	if !reflect.DeepEqual(applied, names) {
		t.Errorf("pickFeatures = %v, want everything standing carried through untouched", applied)
	}
	if !strings.Contains(skipped.String(), "nothing to add") {
		t.Errorf("pickFeatures said %q, want it to say why it asked nothing", skipped.String())
	}
}

func TestWithDependencies(t *testing.T) {
	t.Parallel()

	got := withDependencies(testCatalogue(), []string{featureCloudflareEdge})
	if want := []string{featureISR, featureCloudflareEdge}; !reflect.DeepEqual(got, want) {
		t.Errorf("withDependencies = %v, want %v", got, want)
	}
}

func TestPrintApplied(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	printApplied(&out, testCatalogue(), []string{featureISR, featureCloudflareEdge}, []string{featureCloudflareEdge})
	got := out.String()
	if !strings.HasPrefix(got, "✓ Selected isr ") {
		t.Errorf("printApplied = %q, want the tick and the applied set", got)
	}
	if !strings.Contains(got, featureCloudflareEdge) || !strings.Contains(got, "needed by "+featureCloudflareEdge) {
		t.Errorf("printApplied = %q, want the pulled-in dependency named by what needs it", got)
	}
	if strings.HasPrefix(got, "\x1b[") {
		t.Errorf("printApplied = %q, want the tick left unpainted when the writer is not a terminal", got)
	}

	var empty strings.Builder
	printApplied(&empty, testCatalogue(), nil, nil)
	if empty.String() != "No features will be applied.\n" {
		t.Errorf("printApplied = %q, want the empty set to say so", empty.String())
	}
}
