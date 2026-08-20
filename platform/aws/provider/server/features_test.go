package server

import (
	"slices"
	"strings"
	"testing"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

func TestMissingFeatures(t *testing.T) {
	t.Parallel()

	carrying := func(names ...string) bootstrap.Deployed {
		set := bootstrap.FeatureSet{}
		for _, name := range names {
			set[name] = true
		}
		return bootstrap.Deployed{Present: true, Features: set}
	}

	t.Run("a substrate carrying what the project needs waves it through", func(t *testing.T) {
		t.Parallel()
		deployed := carrying(bootstrap.FeatureISR, bootstrap.FeatureImageOptimization)
		if err := missingFeatures(deployed, []string{bootstrap.FeatureISR}, false); err != nil {
			t.Errorf("missingFeatures = %v, want the deploy admitted", err)
		}
	})

	t.Run("a project needing nothing deploys onto a bare substrate", func(t *testing.T) {
		t.Parallel()
		if err := missingFeatures(carrying(), nil, false); err != nil {
			t.Errorf("missingFeatures = %v, want the deploy admitted", err)
		}
	})

	t.Run("what is missing and the command that fixes it are both named", func(t *testing.T) {
		t.Parallel()
		deployed := carrying(bootstrap.FeatureISR)
		err := missingFeatures(deployed, []string{bootstrap.FeatureImageOptimization, bootstrap.FeatureISR}, false)
		if err == nil {
			t.Fatal("a deploy needing a feature this substrate lacks was admitted")
		}
		if !strings.Contains(err.Error(), bootstrap.FeatureImageOptimization) {
			t.Errorf("error %q does not name what is missing", err)
		}
		want := "ocel bootstrap --features " + strings.Join([]string{bootstrap.FeatureImageOptimization, bootstrap.FeatureISR}, ",")
		if !strings.Contains(err.Error(), want) {
			t.Errorf("error %q does not carry `%s`: the remediation must keep what is already there", err, want)
		}
	})

	t.Run("the preview substrate is remediated with its own command", func(t *testing.T) {
		t.Parallel()
		err := missingFeatures(carrying(), []string{bootstrap.FeatureISR}, true)
		if err == nil {
			t.Fatal("a preview deploy needing a feature this substrate lacks was admitted")
		}
		if !strings.Contains(err.Error(), "ocel bootstrap --preview --features "+bootstrap.FeatureISR) {
			t.Errorf("error %q does not name the preview bootstrap", err)
		}
	})

	t.Run("a name asked for twice is named once", func(t *testing.T) {
		t.Parallel()
		err := missingFeatures(carrying(), []string{bootstrap.FeatureISR, bootstrap.FeatureISR}, false)
		if err == nil {
			t.Fatal("a deploy needing a feature this substrate lacks was admitted")
		}
		if strings.Count(err.Error(), bootstrap.FeatureISR) != 2 {
			t.Errorf("error %q repeats a name; it appears once in the list and once in the command", err)
		}
	})
}

func TestDescribedBootstrap(t *testing.T) {
	t.Parallel()

	find := func(t *testing.T, resp *contractv1.DescribeBootstrapResponse, name string) *contractv1.Feature {
		t.Helper()
		for _, f := range resp.GetFeatures() {
			if f.GetName() == name {
				return f
			}
		}
		t.Fatalf("no feature named %q among %v", name, resp.GetFeatures())
		return nil
	}

	t.Run("it offers the whole catalogue, enabled by what is standing", func(t *testing.T) {
		t.Parallel()
		deployed := bootstrap.Deployed{Present: true, Features: bootstrap.FeatureSet{bootstrap.FeatureISR: true}}
		resp := describedBootstrap(deployed, nil)

		if len(resp.GetFeatures()) != len(bootstrap.Catalogue()) {
			t.Fatalf("described %d features, want the whole catalogue", len(resp.GetFeatures()))
		}
		if !find(t, resp, bootstrap.FeatureISR).GetEnabled() {
			t.Error("a feature whose stack is standing reads as disabled")
		}
		if find(t, resp, bootstrap.FeatureImageOptimization).GetEnabled() {
			t.Error("a feature with no stack reads as enabled")
		}
		if summary := find(t, resp, bootstrap.FeatureISR).GetSummary(); summary == "" {
			t.Error("a feature is offered with nothing to pick it by")
		}
		if deps := find(t, resp, bootstrap.FeatureCloudflareEdge).GetDependsOn(); !slices.Contains(deps, bootstrap.FeatureISR) {
			t.Errorf("cloudflare-edge depends on %v, want isr among them", deps)
		}
	})

	t.Run("each feature names only the projects that recorded needing it", func(t *testing.T) {
		t.Parallel()
		recorded := map[string][]string{
			"shop":    {bootstrap.FeatureISR, bootstrap.FeatureImageOptimization},
			"billing": {bootstrap.FeatureISR},
			"wiki":    nil,
		}
		resp := describedBootstrap(bootstrap.Deployed{Present: true, Features: bootstrap.FeatureSet{}}, recorded)

		if got, want := find(t, resp, bootstrap.FeatureISR).GetDependents(), []string{"billing", "shop"}; !slices.Equal(got, want) {
			t.Errorf("isr dependents = %v, want %v", got, want)
		}
		if got, want := find(t, resp, bootstrap.FeatureImageOptimization).GetDependents(), []string{"shop"}; !slices.Equal(got, want) {
			t.Errorf("image-optimization dependents = %v, want %v", got, want)
		}
		if got := find(t, resp, bootstrap.FeatureCloudflareEdge).GetDependents(); len(got) != 0 {
			t.Errorf("cloudflare-edge dependents = %v, want none", got)
		}
	})

	t.Run("an unbootstrapped account still offers the catalogue", func(t *testing.T) {
		t.Parallel()
		resp := describedBootstrap(bootstrap.Deployed{Features: bootstrap.FeatureSet{}}, nil)
		if len(resp.GetFeatures()) != len(bootstrap.Catalogue()) {
			t.Errorf("described %d features, want the whole catalogue: the picker has to show them", len(resp.GetFeatures()))
		}
		for _, f := range resp.GetFeatures() {
			if f.GetEnabled() {
				t.Errorf("%s reads as enabled with no bootstrap at all", f.GetName())
			}
		}
	})
}
