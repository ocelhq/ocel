package providerkit

import (
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

func TestAPreviewIdentityThatNamesProductionIsRefused(t *testing.T) {
	t.Parallel()

	_, err := envName(&environmentv1.Environment{
		Tier:     environmentv1.Tier_TIER_PREVIEW,
		Identity: ProductionEnv,
	})

	if err == nil {
		t.Fatalf("envName() took %q as a preview identity, and every name a provider builds from the environment "+
			"would then read as production's", ProductionEnv)
	}
	if code, refused := RefusedCode(err); !refused || code != CodeInvalid {
		t.Errorf("envName() code = %v, want %v", code, CodeInvalid)
	}
	if !strings.Contains(err.Error(), ProductionEnv) {
		t.Errorf("envName() = %v, want the identity it refused named", err)
	}
}

func TestAPreviewIdentityBesideProductionsIsTaken(t *testing.T) {
	t.Parallel()

	name, err := envName(&environmentv1.Environment{
		Tier:     environmentv1.Tier_TIER_PREVIEW,
		Identity: "prod-1",
	})
	if err != nil {
		t.Fatalf("envName(prod-1) = %v", err)
	}
	if name != "prod-1" {
		t.Errorf("envName(prod-1) = %q, want the identity the caller named", name)
	}
}
