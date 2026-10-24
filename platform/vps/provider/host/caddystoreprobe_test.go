//go:build integration

package host

import (
	"testing"
)

func TestAStorePresentBeforeAnyDomainIsBoundIsATableTheBoxServes(t *testing.T) {
	t.Parallel()

	state := storing()
	state.Claims = nil
	ask := probing(t, state)
	if answer := ask("nothing.example.com"); answer.status != 404 {
		t.Errorf("a box routing a store nothing claims a name for answered %d", answer.status)
	}
}
