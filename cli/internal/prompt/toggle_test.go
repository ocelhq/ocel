package prompt

import (
	"slices"
	"testing"
)

func TestToggle(t *testing.T) {
	t.Parallel()

	options := []Option{{Name: "isr"}, {Name: "image-optimization"}, {Name: "cloudflare-edge"}}
	got, err := toggle(options, []string{"isr"}, "1, 3")
	if err != nil {
		t.Fatalf("toggle: %v", err)
	}
	if want := []string{"cloudflare-edge"}; !slices.Equal(got, want) {
		t.Errorf("toggle = %v, want the first off and the third on", got)
	}
	if _, err := toggle(options, nil, "9"); err == nil {
		t.Error("toggle accepted a number no option answers to")
	}
}
