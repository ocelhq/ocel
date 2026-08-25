package providerkit

import (
	"errors"
	"reflect"
	"strings"
	"testing"
)

var testCatalogue = []Feature{
	{Name: "isr", Summary: "incremental regeneration", Needs: []string{NeedsFrameworkPrefix + "next"}},
	{Name: "image-optimization", Summary: "image optimizer", Needs: []string{NeedsFrameworkPrefix + "next"}},
	{Name: "cloudflare-edge", Summary: "cloudflare front", DependsOn: []string{"isr"}, Needs: []string{NeedsEdgePrefix + "cloudflare"}},
}

func TestFeatureClosure(t *testing.T) {
	t.Parallel()

	t.Run("pulls in what a feature depends on", func(t *testing.T) {
		t.Parallel()

		got, err := featureClosure(testCatalogue, []string{"cloudflare-edge"})
		if err != nil {
			t.Fatalf("featureClosure() = %v", err)
		}
		if want := []string{"isr", "cloudflare-edge"}; !reflect.DeepEqual(got, want) {
			t.Errorf("featureClosure() = %v, want %v", got, want)
		}
	})

	t.Run("orders by the catalogue however the caller listed them", func(t *testing.T) {
		t.Parallel()

		got, err := featureClosure(testCatalogue, []string{"cloudflare-edge", "image-optimization", "isr", "isr"})
		if err != nil {
			t.Fatalf("featureClosure() = %v", err)
		}
		if want := []string{"isr", "image-optimization", "cloudflare-edge"}; !reflect.DeepEqual(got, want) {
			t.Errorf("featureClosure() = %v, want %v", got, want)
		}
	})

	t.Run("nothing requested is nothing resolved", func(t *testing.T) {
		t.Parallel()

		got, err := featureClosure(testCatalogue, nil)
		if err != nil || len(got) != 0 {
			t.Fatalf("featureClosure(nil) = %v, %v, want none", got, err)
		}
	})

	t.Run("an unknown name is refused and names what there is", func(t *testing.T) {
		t.Parallel()

		_, err := featureClosure(testCatalogue, []string{"quantum-edge"})
		var refusal Refusal
		if !errors.As(err, &refusal) || refusal.Code != CodeInvalid {
			t.Fatalf("featureClosure() = %v, want a %s refusal", err, CodeInvalid)
		}
		for _, want := range []string{"quantum-edge", "isr", "image-optimization", "cloudflare-edge"} {
			if !strings.Contains(refusal.Message, want) {
				t.Errorf("refusal %q does not name %q", refusal.Message, want)
			}
		}
	})

	t.Run("a dependency the catalogue does not carry names the feature that wanted it", func(t *testing.T) {
		t.Parallel()

		_, err := featureClosure([]Feature{{Name: "isr", DependsOn: []string{"gone"}}}, []string{"isr"})
		if err == nil || !strings.Contains(err.Error(), "isr depends on \"gone\"") {
			t.Fatalf("featureClosure() = %v, want it to name the feature and the dependency", err)
		}
	})
}

func TestFeatureLevels(t *testing.T) {
	t.Parallel()

	t.Run("independent features share a level", func(t *testing.T) {
		t.Parallel()

		got, err := FeatureLevels(testCatalogue, []string{"isr", "image-optimization", "cloudflare-edge"})
		if err != nil {
			t.Fatalf("FeatureLevels() = %v", err)
		}
		want := [][]string{{"isr", "image-optimization"}, {"cloudflare-edge"}}
		if !reflect.DeepEqual(got, want) {
			t.Errorf("FeatureLevels() = %v, want %v", got, want)
		}
	})

	t.Run("a level never precedes what it depends on", func(t *testing.T) {
		t.Parallel()

		got, err := FeatureLevels(testCatalogue, []string{"cloudflare-edge", "isr"})
		if err != nil {
			t.Fatalf("FeatureLevels() = %v", err)
		}
		if want := [][]string{{"isr"}, {"cloudflare-edge"}}; !reflect.DeepEqual(got, want) {
			t.Errorf("FeatureLevels() = %v, want %v", got, want)
		}
	})

	t.Run("nothing requested is no level at all", func(t *testing.T) {
		t.Parallel()

		got, err := FeatureLevels(testCatalogue, nil)
		if err != nil || len(got) != 0 {
			t.Fatalf("FeatureLevels(nil) = %v, %v, want none", got, err)
		}
	})

	t.Run("a set no order satisfies is named, not silently cut short", func(t *testing.T) {
		t.Parallel()

		cyclic := []Feature{
			{Name: "hen", DependsOn: []string{"egg"}},
			{Name: "egg", DependsOn: []string{"hen"}},
		}
		_, err := FeatureLevels(cyclic, []string{"hen", "egg"})
		if err == nil {
			t.Fatal("FeatureLevels() dropped a cyclic set instead of refusing it")
		}
		for _, want := range []string{"hen", "egg"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("error %q does not name %q", err, want)
			}
		}
	})
}

func TestFeatureDeleteOrder(t *testing.T) {
	t.Parallel()

	got, err := featureDeleteOrder(testCatalogue, []string{"isr", "cloudflare-edge"})
	if err != nil {
		t.Fatalf("featureDeleteOrder() = %v", err)
	}
	if want := []string{"cloudflare-edge", "isr"}; !reflect.DeepEqual(got, want) {
		t.Errorf("featureDeleteOrder() = %v, want what depends on a feature to go first", got)
	}
}

func TestFeatureRemoval(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name     string
		standing []string
		named    []string
		want     []string
		wantErr  bool
	}{
		{
			name:     "a run that names nothing removes nothing",
			standing: []string{"isr", "image-optimization"},
		},
		{
			name:     "a standing name left out is left alone",
			standing: []string{"isr", "image-optimization"},
			named:    []string{"image-optimization"},
			want:     []string{"image-optimization"},
		},
		{
			name:     "what stands on a removed feature goes with it",
			standing: []string{"isr", "cloudflare-edge"},
			named:    []string{"isr"},
			want:     []string{"isr", "cloudflare-edge"},
		},
		{
			name:     "a name that is not standing is nothing to remove",
			standing: []string{"isr"},
			named:    []string{"image-optimization"},
		},
		{
			name:     "a dependent that was never there is not conjured up",
			standing: []string{"isr"},
			named:    []string{"isr"},
			want:     []string{"isr"},
		},
		{
			name:     "a name no catalogue offers is refused",
			standing: []string{"isr"},
			named:    []string{"nonesuch"},
			wantErr:  true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := featureRemoval(testCatalogue, tc.standing, tc.named)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("featureRemoval() = %v, want a refusal naming what this provider does not offer", got)
				}
				return
			}
			if err != nil {
				t.Fatalf("featureRemoval() = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("featureRemoval() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestRequiredFeatures(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name       string
		frameworks []string
		edge       string
		want       []string
	}{
		{name: "a project needing nothing needs no feature"},
		{
			name:       "a framework pulls the features that name it",
			frameworks: []string{"next"},
			want:       []string{"isr", "image-optimization"},
		},
		{
			name: "an edge pulls the feature that names it, and what it depends on",
			edge: "cloudflare",
			want: []string{"isr", "cloudflare-edge"},
		},
		{
			name:       "frameworks and edge together are one set",
			frameworks: []string{"next"},
			edge:       "cloudflare",
			want:       []string{"isr", "image-optimization", "cloudflare-edge"},
		},
		{
			name:       "a framework no feature names pulls nothing",
			frameworks: []string{"astro"},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := RequiredFeatures(testCatalogue, tc.frameworks, tc.edge)
			if err != nil {
				t.Fatalf("RequiredFeatures() = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("RequiredFeatures(%v, %q) = %v, want %v", tc.frameworks, tc.edge, got, tc.want)
			}
		})
	}
}

func TestMissingFeatures(t *testing.T) {
	t.Parallel()

	got := missingFeatures([]string{"isr"}, []string{"image-optimization", "isr", "image-optimization"})
	if want := []string{"image-optimization"}; !reflect.DeepEqual(got, want) {
		t.Errorf("missingFeatures() = %v, want %v", got, want)
	}
}
