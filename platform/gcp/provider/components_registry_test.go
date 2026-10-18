package gcp

import "testing"

func TestEveryKindARegisteredComponentNamesIsServed(t *testing.T) {
	t.Parallel()
	if unserved := (&Provider{}).components().Unserved(); len(unserved) != 0 {
		t.Errorf("the registry names %v, which no resources.Hooks field serves, so their plan, provision and remove are dropped", unserved)
	}
}
