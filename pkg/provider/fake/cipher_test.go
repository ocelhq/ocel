package fake_test

import (
	"context"
	"errors"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/seal"
)

func TestACipherToldToRefuseOpeningRefusesWhatItSealed(t *testing.T) {
	t.Parallel()

	ctx := context.Background()
	cipher := fake.NewCipher()
	bound := seal.AssociatedData{{Name: "key", Value: "STRIPE_API_KEY"}}
	sealed, err := cipher.Seal(ctx, environment.TierProduction, bound, []byte("sk_live"))
	if err != nil {
		t.Fatal(err)
	}

	unreachable := errors.New("the key service is unreachable")
	cipher.RefuseOpening(unreachable)
	if _, err := cipher.Open(ctx, environment.TierProduction, bound, sealed); !errors.Is(err, unreachable) {
		t.Errorf("Open() error = %v, want the refusal it was told to answer", err)
	}

	cipher.RefuseOpening(nil)
	opened, err := cipher.Open(ctx, environment.TierProduction, bound, sealed)
	if err != nil || string(opened) != "sk_live" {
		t.Errorf("Open() = %q, %v, want the sealed value once the refusal is lifted", opened, err)
	}
}
