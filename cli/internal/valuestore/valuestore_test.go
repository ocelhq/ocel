package valuestore

import (
	"errors"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestAWriteThatLostAVersionRaceIsAStaleValue(t *testing.T) {
	t.Run("a version conflict is a stale value", func(t *testing.T) {
		conflict := connect.NewError(connect.CodeAborted, errors.New("envvars: stale version"))
		if err := staleValueError(conflict); !errors.Is(err, variables.ErrStaleValue) {
			t.Errorf("staleValueError = %v, want variables.ErrStaleValue", err)
		}
	})

	t.Run("a refusal keeps what it says", func(t *testing.T) {
		refused := provider.RefusalError(refusal.Refuse(refusal.CodeBusy,
			"the production bootstrap has no key to seal a value under"))

		err := staleValueError(refused)
		if errors.Is(err, variables.ErrStaleValue) {
			t.Fatalf("staleValueError = %v, want the refusal itself: nothing about it is a version conflict", err)
		}
		if !strings.Contains(err.Error(), "no key to seal a value under") {
			t.Errorf("staleValueError = %v, want what the provider refused with", err)
		}
	})

	t.Run("nothing is nothing", func(t *testing.T) {
		if err := staleValueError(nil); err != nil {
			t.Errorf("staleValueError(nil) = %v, want nil", err)
		}
	})
}
