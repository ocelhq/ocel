package host

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
)

func TestAVPSBootstrapsBaseHoldsEveryItemTheBoxIsSetUpWith(t *testing.T) {
	t.Parallel()
	read := currentHost()

	parts, err := Bootstrap{}.DescribeParts(context.Background(), provider.BootstrapDescription{
		Tier: read.Tier, Present: true, VendorState: read,
		Stacks: []provider.BootstrapStack{{Name: "root@box", Present: true, DigestCurrent: true}},
	})
	if err != nil {
		t.Fatal(err)
	}

	if len(parts) != 1 || parts[0].Name != provider.BasePart || !parts[0].Current {
		t.Fatalf("DescribeParts() = %+v, want the base alone, current", parts)
	}
	if got, want := len(parts[0].Resources), len(read.Items()); got != want {
		t.Errorf("base holds %d resources, want one per item (%d)", got, want)
	}
}

func TestAVPSWithNoBootstrapHasNoParts(t *testing.T) {
	t.Parallel()
	read := currentHost()
	read.Present = false

	parts, err := Bootstrap{}.DescribeParts(context.Background(), provider.BootstrapDescription{Tier: read.Tier, VendorState: read})

	if err != nil || len(parts) != 0 {
		t.Errorf("DescribeParts() = %+v, %v, want none", parts, err)
	}
}
