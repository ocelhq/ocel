package network

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestANetworkStillInUseIsRefusedNamingThePartsInItByTheNamesRemoveTakes(t *testing.T) {
	t.Parallel()

	stacks := &Stacks{PartsByConsumer: map[string]string{"ocel-containers": "containers", "ocel-bastion": "bastion"}}
	err := stacks.RefuseInUse(environment.TierProduction, []string{"ocel-bastion", "ocel-containers", "shop"})
	var refused refusal.Refusal
	if !errors.As(err, &refused) {
		t.Fatalf("RefuseInUse() = %v, want a refusal", err)
	}
	for _, named := range []string{"the bastion and containers parts", provider.BootstrapCommand(environment.TierProduction) + " --remove <part>", "environments of shop"} {
		if !strings.Contains(refused.Message, named) {
			t.Errorf("refusal %q does not say %q", refused.Message, named)
		}
	}
	if strings.Contains(refused.Message, "--remove ocel-") || strings.Contains(refused.Message, "environments of ocel-") {
		t.Errorf("refusal %q tells the user to act on a consumer's slug, which no command takes", refused.Message)
	}
}
