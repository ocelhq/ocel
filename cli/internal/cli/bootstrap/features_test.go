package bootstrap

import (
	"context"
	"reflect"
	"slices"
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const (
	featureISR               = "isr"
	featureImageOptimization = "image-optimization"
	featureCloudflareEdge    = "cloudflare-edge"
	featureCloudFrontEdge    = "cloudfront-edge"
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
		{Name: featureCloudflareEdge, Summary: "a Cloudflare front", DependsOn: []string{featureISR},
			Needs: []string{needsEdgePrefix + "cloudflare"}, Enabled: on(featureCloudflareEdge)},
		{Name: featureCloudFrontEdge, Summary: "a CloudFront front",
			Needs: []string{needsEdgePrefix + "cloudfront"}, Enabled: on(featureCloudFrontEdge)},
	}
}

func TestParseFeatureFlag(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		raw  string
		want []string
	}{
		{"all takes everything the provider offers", "all", []string{featureISR, featureImageOptimization, featureCloudflareEdge, featureCloudFrontEdge}},
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

func TestThePickerOffersOnlyWhatIsNotAlreadyThere(t *testing.T) {
	t.Parallel()

	catalogue := testCatalogue(featureISR)
	got := addableFeatures(catalogue, []string{featureISR}, "")
	if want := []string{featureImageOptimization, featureCloudflareEdge, featureCloudFrontEdge}; !reflect.DeepEqual(got, want) {
		t.Errorf("addableFeatures = %v, want %v — an included feature is shown, never offered as a toggle", got, want)
	}

	got = addableFeatures(catalogue, []string{featureISR}, featureCloudflareEdge)
	if want := []string{featureImageOptimization, featureCloudFrontEdge}; !reflect.DeepEqual(got, want) {
		t.Errorf("addableFeatures = %v, want %v — what the project requires is applied, never offered", got, want)
	}
}

func TestAFeatureForAnotherEdgeIsStillOffered(t *testing.T) {
	t.Parallel()

	got := addableFeatures(testCatalogue(), nil, featureCloudFrontEdge)
	if !slices.Contains(got, featureCloudflareEdge) {
		t.Errorf("addableFeatures = %v, want a feature fronting a different edge left on offer: standing one up for a future project is the user's call", got)
	}
}

func TestPrintIncluded(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	printIncluded(&out, testCatalogue(featureISR), []string{featureISR}, environmentv1.Tier_TIER_PRODUCTION)
	got := out.String()
	for _, want := range []string{
		"Included in the production bootstrap:",
		"✓ " + featureISR + "   incremental static regeneration",
		"To take one down: ocel bootstrap production --remove <name>",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("printIncluded = %q, want it to carry %q", got, want)
		}
	}

	var empty strings.Builder
	printIncluded(&empty, testCatalogue(), nil, environmentv1.Tier_TIER_PRODUCTION)
	if empty.String() != "" {
		t.Errorf("printIncluded = %q, want nothing included to print nothing at all", empty.String())
	}
}

func TestPrintRequired(t *testing.T) {
	t.Parallel()

	catalogue := testCatalogue()
	var out strings.Builder
	printRequired(&out, catalogue, requiredFeature(catalogue, nil, "cloudfront"), "cloudfront")
	got := out.String()
	for _, want := range []string{
		"Required by this project:",
		"✓ " + featureCloudFrontEdge + "   a CloudFront front",
		"Your edge is cloudfront. Change it in ocel.config.ts.",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("printRequired = %q, want it to carry %q", got, want)
		}
	}

	for _, tc := range []struct {
		name     string
		included []string
		kind     string
	}{
		{"no edge kind", nil, ""},
		{"a kind no feature fronts", nil, "quantum"},
		{"a kind whose feature is already included", []string{featureCloudFrontEdge}, "cloudfront"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			var quiet strings.Builder
			printRequired(&quiet, catalogue, requiredFeature(catalogue, tc.included, tc.kind), tc.kind)
			if quiet.String() != "" {
				t.Errorf("printRequired = %q, want nothing", quiet.String())
			}
		})
	}
}

func TestTheInteractivePathAppliesWhatTheEdgeRequires(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	names := []string{featureISR, featureImageOptimization, featureCloudflareEdge}
	applied, selected, err := pickFeatures(context.Background(), testCatalogue(names...), names, nil, "cloudfront",
		environmentv1.Tier_TIER_PRODUCTION, &out)
	if err != nil || !selected {
		t.Fatalf("pickFeatures = %v, %v, want the only feature left to add applied without a prompt", selected, err)
	}
	want := append(slices.Clone(names), featureCloudFrontEdge)
	if !reflect.DeepEqual(applied, inCatalogueOrder(testCatalogue(), want)) {
		t.Errorf("pickFeatures = %v, want the edge's feature applied on top of what is included", applied)
	}
	if !strings.Contains(out.String(), "✓ Adding "+featureCloudFrontEdge) {
		t.Errorf("pickFeatures said %q, want the required feature named as added", out.String())
	}
}

func TestThePickerSaysWhenThereIsNothingLeft(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	names := []string{featureISR, featureImageOptimization, featureCloudflareEdge, featureCloudFrontEdge}
	applied, selected, err := pickFeatures(context.Background(), testCatalogue(names...), names, nil, "",
		environmentv1.Tier_TIER_PRODUCTION, &out)
	if err != nil || !selected {
		t.Fatalf("pickFeatures = %v, %v, want an account with nothing left to add to skip the prompt", selected, err)
	}
	if !reflect.DeepEqual(applied, names) {
		t.Errorf("pickFeatures = %v, want everything included carried through untouched", applied)
	}
	if !strings.Contains(out.String(), "Everything is already included.") {
		t.Errorf("pickFeatures said %q, want it to say why it asked nothing", out.String())
	}
}

func TestWithDependencies(t *testing.T) {
	t.Parallel()

	got := withDependencies(testCatalogue(), []string{featureCloudflareEdge})
	if want := []string{featureISR, featureCloudflareEdge}; !reflect.DeepEqual(got, want) {
		t.Errorf("withDependencies = %v, want %v", got, want)
	}
}

func TestPrintAdded(t *testing.T) {
	t.Parallel()

	var out strings.Builder
	printAdded(&out, testCatalogue(), []string{featureISR, featureCloudflareEdge}, nil, []string{featureCloudflareEdge})
	got := out.String()
	if want := "✓ Adding " + featureCloudflareEdge + "\n"; !strings.HasPrefix(got, want) {
		t.Errorf("printAdded = %q, want it to open with %q — only what was picked", got, want)
	}
	if want := "  + " + featureISR + " — " + featureCloudflareEdge + " needs it\n"; !strings.Contains(got, want) {
		t.Errorf("printAdded = %q, want the pulled-in dependency named by what needs it", got)
	}
	if strings.HasPrefix(got, "\x1b[") {
		t.Errorf("printAdded = %q, want the tick left unpainted when the writer is not a terminal", got)
	}

	var shared strings.Builder
	catalogue := []*contractv1.Feature{
		{Name: featureISR},
		{Name: featureCloudflareEdge, DependsOn: []string{featureISR}},
		{Name: featureCloudFrontEdge, DependsOn: []string{featureISR}},
	}
	picked := []string{featureCloudflareEdge, featureCloudFrontEdge}
	printAdded(&shared, catalogue, withDependencies(catalogue, picked), nil, picked)
	if want := "  + " + featureISR + " — " + featureCloudflareEdge + ", " + featureCloudFrontEdge + " need it\n"; !strings.Contains(shared.String(), want) {
		t.Errorf("printAdded = %q, want %q", shared.String(), want)
	}

	var included strings.Builder
	printAdded(&included, testCatalogue(featureISR), []string{featureISR, featureCloudflareEdge}, []string{featureISR}, []string{featureCloudflareEdge})
	if strings.Contains(included.String(), "  + "+featureISR) {
		t.Errorf("printAdded = %q, want an already-included feature left out of the block", included.String())
	}

	var empty strings.Builder
	printAdded(&empty, testCatalogue(), nil, nil, nil)
	if empty.String() != "Nothing to add.\n" {
		t.Errorf("printAdded = %q, want the empty set to say so", empty.String())
	}
}
