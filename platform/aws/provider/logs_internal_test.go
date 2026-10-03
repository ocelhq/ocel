package aws

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAnEventLabelOutsideTheTargetsNamesNoTarget(t *testing.T) {
	t.Parallel()

	targets := []provider.LogTarget{{App: "web"}, {App: "api"}}
	if got, ok := targetOfLabel(targets, "1"); !ok || got.App != "api" {
		t.Errorf("targetOfLabel(1) = %+v, %v, want the api target", got, ok)
	}
	for _, label := range []string{"", "web", "-1", "2"} {
		if got, ok := targetOfLabel(targets, label); ok {
			t.Errorf("targetOfLabel(%q) = %+v, want no target", label, got)
		}
	}
}
