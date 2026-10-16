package bootstrapplan

import (
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

var testParts = []provider.Part{
	{Name: "variables-key", Summary: "the key variables are sealed under"},
	{Name: "image-optimization", Summary: "image optimizer"},
	{Name: "cloudflare-edge", Summary: "cloudflare front", Edges: []edge.Kind{"cloudflare"}},
}

func TestARemovalTakesTheInstalledPartsItNamesInOfferedOrderAndRefusesOneNotOffered(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name      string
		installed []string
		named     []string
		want      []string
		wantErr   bool
	}{
		{
			name:      "a run that names nothing removes nothing",
			installed: []string{"variables-key", "image-optimization"},
		},
		{
			name:      "an installed name left out is left alone",
			installed: []string{"variables-key", "image-optimization"},
			named:     []string{"image-optimization"},
			want:      []string{"image-optimization"},
		},
		{
			name:      "the parts named go in the order they are offered",
			installed: []string{"variables-key", "cloudflare-edge"},
			named:     []string{"cloudflare-edge", "variables-key"},
			want:      []string{"variables-key", "cloudflare-edge"},
		},
		{
			name:      "a name that is not installed is nothing to remove",
			installed: []string{"variables-key"},
			named:     []string{"image-optimization"},
		},
		{
			name:      "a name this provider does not offer is refused",
			installed: []string{"variables-key"},
			named:     []string{"nonesuch"},
			wantErr:   true,
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := PartsToRemove(testParts, tc.installed, tc.named)
			if tc.wantErr {
				var refused refusal.Refusal
				if !errors.As(err, &refused) || refused.Code != refusal.CodeInvalid || !strings.Contains(refused.Message, "variables-key, image-optimization, cloudflare-edge") {
					t.Fatalf("PartsToRemove() = %v, %v, want an invalid refusal naming what this provider offers", got, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("PartsToRemove() = %v", err)
			}
			if !reflect.DeepEqual(got, tc.want) {
				t.Errorf("PartsToRemove() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAProjectRequiresOnlyThePartThatFrontsItsEdge(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name string
		edge edge.Kind
		want []string
	}{
		{name: "a project with no edge requires no part"},
		{name: "an edge requires the part that fronts it", edge: "cloudflare", want: []string{"cloudflare-edge"}},
		{name: "an edge no part fronts requires nothing", edge: "direct"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			if got := RequiredParts(testParts, tc.edge); !reflect.DeepEqual(got, tc.want) {
				t.Errorf("RequiredParts(%q) = %v, want %v", tc.edge, got, tc.want)
			}
		})
	}
}

func TestThePartsMissingAreTheRequiredOnesNotInstalledEachNamedOnce(t *testing.T) {
	t.Parallel()

	got := MissingParts([]string{"variables-key"}, []string{"image-optimization", "variables-key", "image-optimization"})
	if want := []string{"image-optimization"}; !reflect.DeepEqual(got, want) {
		t.Errorf("MissingParts() = %v, want %v", got, want)
	}
}
