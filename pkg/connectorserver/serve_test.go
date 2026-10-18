package connectorserver

import (
	"strings"
	"testing"
)

func TestAConnectorWithNoKeyValuesNamesTheFieldItLacks(t *testing.T) {
	t.Parallel()

	err := Serve(Spec{})
	if err == nil || !strings.Contains(err.Error(), "Spec.VariableStore.KeyValues") {
		t.Errorf("Serve() with no store = %v, want it to name Spec.VariableStore.KeyValues", err)
	}
}
