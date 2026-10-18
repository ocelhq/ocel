package host

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
)

func TestAnApplyWritesEveryItemItsPlanCountsSoTheDeployLoginGetsItsCheckedDocker(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	missing := DockerAccessItems(front.Proxy{})
	older := bootstrappedOn(t, tier)
	older.installed[tier] = slices.DeleteFunc(older.installed[tier], func(item machine.Item) bool {
		return slices.ContainsFunc(missing, func(gone machine.Item) bool { return gone.ID() == item.ID() })
	})
	progress := &said{}
	if err := NewBootstrap(older.host(), testVendor, "shop").Apply(context.Background(),
		provider.BootstrapRequest{Tier: tier, WrittenBy: "the-suite"}, progress); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	for _, item := range missing {
		if progress.at("Installed "+item.Phrase()) < 0 {
			t.Errorf("an apply never wrote %s, though the plan and the stamp both count it:\n%s", item.ID(), strings.Join(progress.lines, "\n"))
		}
	}
}
