package envwire

import (
	"errors"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/varsui"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

func TestStaleOrBroken(t *testing.T) {
	t.Run("a version conflict is a stale value", func(t *testing.T) {
		conflict := connect.NewError(connect.CodeFailedPrecondition, errors.New("values: stale version"))
		if err := staleOrBroken(conflict); !errors.Is(err, varsui.ErrStaleValue) {
			t.Errorf("staleOrBroken = %v, want varsui.ErrStaleValue", err)
		}
	})

	t.Run("a refusal keeps what it says", func(t *testing.T) {
		refusal := providerkit.RefusalError(providerkit.Refuse(providerkit.CodeNotReady,
			"the production bootstrap holds no key to seal a value under"))

		err := staleOrBroken(refusal)
		if errors.Is(err, varsui.ErrStaleValue) {
			t.Fatalf("staleOrBroken = %v, want the refusal itself: nothing about it is a version conflict", err)
		}
		if !strings.Contains(err.Error(), "no key to seal a value under") {
			t.Errorf("staleOrBroken = %v, want what the provider refused with", err)
		}
	})

	t.Run("nothing is nothing", func(t *testing.T) {
		if err := staleOrBroken(nil); err != nil {
			t.Errorf("staleOrBroken(nil) = %v, want nil", err)
		}
	})
}
