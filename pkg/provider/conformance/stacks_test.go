package conformance

import (
	"encoding/json"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/stackrecords"
)

func recordOf(t *testing.T, bindings ...provider.Binding) []byte {
	t.Helper()
	raw, err := json.Marshal(stackrecords.Stack{Kind: provider.StackInfra, Bindings: bindings})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func TestARecordedPasswordJSONEscapesIsStillFoundInTheStackRecord(t *testing.T) {
	db := provider.Binding{Type: provider.BindingPostgres, Name: "db", Properties: map[string]string{
		provider.PropertyHost: "db.internal", provider.PropertyPassword: `p&ss<w>"rd\`,
	}}
	if faults := findRecordedSecrets(recordOf(t, db)); len(faults) != 1 {
		t.Errorf("findRecordedSecrets() = %q, want the one postgres password the record holds", faults)
	}
}

func TestAStackRecordKeepingOnlyAHostIsNotFoundHoldingAPassword(t *testing.T) {
	db := provider.Binding{Type: provider.BindingPostgres, Name: "db", Properties: map[string]string{provider.PropertyHost: "db.internal"}}
	if faults := findRecordedSecrets(recordOf(t, db)); len(faults) != 0 {
		t.Errorf("findRecordedSecrets() = %q, want none: the record keeps the host and no password", faults)
	}
}
