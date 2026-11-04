package gcp

import (
	"testing"

	"github.com/ocelhq/ocel/platform/gcp/provider/control"
)

func TestEveryKindARegisteredComponentNamesIsServed(t *testing.T) {
	t.Parallel()
	if unserved := newProvider(&control.Provider{}).Components().Unserved(); len(unserved) != 0 {
		t.Errorf("the registry names %v, which no resources.Hooks field serves, so their plan, provision and remove are dropped", unserved)
	}
}
