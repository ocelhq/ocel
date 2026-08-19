package bootstrap

import (
	"reflect"
	"strings"
	"testing"
)

func TestResolveFeatures(t *testing.T) {
	t.Run("pulls in what a feature depends on", func(t *testing.T) {
		got, err := resolveFeatures([]string{FeatureCloudflareEdge})
		if err != nil {
			t.Fatalf("resolveFeatures: %v", err)
		}
		if want := []string{FeatureISR, FeatureCloudflareEdge}; !reflect.DeepEqual(got, want) {
			t.Errorf("resolveFeatures = %v, want %v", got, want)
		}
	})

	t.Run("orders by the registry however the caller listed them", func(t *testing.T) {
		got, err := resolveFeatures([]string{FeatureCloudflareEdge, FeatureImageOptimization, FeatureISR})
		if err != nil {
			t.Fatalf("resolveFeatures: %v", err)
		}
		want := []string{FeatureISR, FeatureImageOptimization, FeatureCloudflareEdge}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("resolveFeatures = %v, want %v", got, want)
		}
	})

	t.Run("repeats collapse", func(t *testing.T) {
		got, err := resolveFeatures([]string{FeatureISR, FeatureISR})
		if err != nil {
			t.Fatalf("resolveFeatures: %v", err)
		}
		if want := []string{FeatureISR}; !reflect.DeepEqual(got, want) {
			t.Errorf("resolveFeatures = %v, want %v", got, want)
		}
	})

	t.Run("nothing requested is nothing resolved", func(t *testing.T) {
		got, err := resolveFeatures(nil)
		if err != nil {
			t.Fatalf("resolveFeatures: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("resolveFeatures = %v, want none", got)
		}
	})

	t.Run("an unknown name names what there is", func(t *testing.T) {
		_, err := resolveFeatures([]string{"quantum-edge"})
		if err == nil {
			t.Fatal("resolveFeatures accepted a feature this provider has never heard of")
		}
		for _, want := range []string{"quantum-edge", FeatureISR, FeatureImageOptimization, FeatureCloudflareEdge} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err, want)
			}
		}
	})
}

func TestFeatureLevels(t *testing.T) {
	t.Run("independent features share a level", func(t *testing.T) {
		got, err := featureLevels([]string{FeatureISR, FeatureImageOptimization, FeatureCloudflareEdge})
		if err != nil {
			t.Fatalf("featureLevels: %v", err)
		}
		want := [][]string{{FeatureISR, FeatureImageOptimization}, {FeatureCloudflareEdge}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("featureLevels = %v, want %v", got, want)
		}
	})

	t.Run("a level never precedes what it depends on", func(t *testing.T) {
		got, err := featureLevels([]string{FeatureCloudflareEdge, FeatureISR})
		if err != nil {
			t.Fatalf("featureLevels: %v", err)
		}
		want := [][]string{{FeatureISR}, {FeatureCloudflareEdge}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("featureLevels = %v, want %v", got, want)
		}
	})

	t.Run("nothing requested is no level at all", func(t *testing.T) {
		got, err := featureLevels(nil)
		if err != nil {
			t.Fatalf("featureLevels: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("featureLevels = %v, want none", got)
		}
	})

	t.Run("a set no order satisfies is named, not silently cut short", func(t *testing.T) {
		previous := featureRegistry
		featureRegistry = []feature{
			{name: "hen", dependsOn: []string{"egg"}},
			{name: "egg", dependsOn: []string{"hen"}},
		}
		t.Cleanup(func() { featureRegistry = previous })

		_, err := featureLevels([]string{"hen", "egg"})
		if err == nil {
			t.Fatal("featureLevels dropped a cyclic set instead of refusing it")
		}
		for _, want := range []string{"hen", "egg"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err, want)
			}
		}
	})
}

func TestFeatureDiff(t *testing.T) {
	for _, tc := range []struct {
		name      string
		deployed  FeatureSet
		requested []string
		wantAdd   []string
		wantDrop  []string
	}{
		{
			name:      "first bootstrap adds everything asked for",
			deployed:  FeatureSet{},
			requested: []string{FeatureISR, FeatureCloudflareEdge},
			wantAdd:   []string{FeatureISR, FeatureCloudflareEdge},
		},
		{
			name:      "a re-run of the same set adds and drops nothing",
			deployed:  FeatureSet{FeatureISR: true},
			requested: []string{FeatureISR},
		},
		{
			name:      "a name left out of the set is a drop",
			deployed:  FeatureSet{FeatureISR: true, FeatureImageOptimization: true},
			requested: []string{FeatureISR},
			wantDrop:  []string{FeatureImageOptimization},
		},
		{
			name:     "asking for nothing drops all of them",
			deployed: FeatureSet{FeatureISR: true, FeatureCloudflareEdge: true},
			wantDrop: []string{FeatureISR, FeatureCloudflareEdge},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			add, drop := featureDiff(tc.deployed, tc.requested)
			if !reflect.DeepEqual(add, tc.wantAdd) {
				t.Errorf("add = %v, want %v", add, tc.wantAdd)
			}
			if !reflect.DeepEqual(drop, tc.wantDrop) {
				t.Errorf("drop = %v, want %v", drop, tc.wantDrop)
			}
		})
	}
}

func TestDropClosure(t *testing.T) {
	t.Run("takes what is left standing on the dropped feature with it", func(t *testing.T) {
		present := FeatureSet{FeatureISR: true, FeatureCloudflareEdge: true}
		got := dropClosure([]string{FeatureISR}, present)
		if want := []string{FeatureISR, FeatureCloudflareEdge}; !reflect.DeepEqual(got, want) {
			t.Errorf("dropClosure = %v, want %v", got, want)
		}
	})

	t.Run("leaves a dependent that was never there", func(t *testing.T) {
		got := dropClosure([]string{FeatureISR}, FeatureSet{FeatureISR: true})
		if want := []string{FeatureISR}; !reflect.DeepEqual(got, want) {
			t.Errorf("dropClosure = %v, want %v", got, want)
		}
	})
}

func TestProjectsDependingOn(t *testing.T) {
	recorded := map[string][]string{
		"shop":    {FeatureISR, FeatureImageOptimization},
		"billing": {FeatureCloudflareEdge},
		"docs":    nil,
	}

	t.Run("names every project whose deploy recorded the feature", func(t *testing.T) {
		got := projectsDependingOn(recorded, []string{FeatureISR, FeatureCloudflareEdge})
		if want := []string{"billing", "shop"}; !reflect.DeepEqual(got, want) {
			t.Errorf("projectsDependingOn = %v, want %v", got, want)
		}
	})

	t.Run("a feature nothing recorded blocks nothing", func(t *testing.T) {
		if got := projectsDependingOn(recorded, []string{"never-recorded"}); len(got) != 0 {
			t.Errorf("projectsDependingOn = %v, want none", got)
		}
	})
}

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
