package bootstrap

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
)

func TestTheCommandThatEmptiesATierDestroysItsProjectsOnThatTierAlone(t *testing.T) {
	t.Parallel()

	for tier, want := range map[environment.Tier]string{
		environment.TierProduction: "ocel projects destroy --tier production",
		environment.TierPreview:    "ocel projects destroy --tier preview",
	} {
		if got := destroyIn(tier); got != want {
			t.Errorf("destroyIn(%s) = %q, want %q", tier, got, want)
		}
	}
}
