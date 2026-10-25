package bindings

import (
	"testing"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestACustomPropertyReadsAsAVariableValueVerbatimWhenTextAndAsJSONOtherwise(t *testing.T) {
	custom, err := structpb.NewStruct(map[string]any{
		"certificate": "-----BEGIN CERTIFICATE-----\n",
		"port":        8443.0,
		"tls":         true,
		"zones":       []any{"a", "b"},
		"limits":      map[string]any{"rps": 10.0},
	})
	if err != nil {
		t.Fatal(err)
	}
	binding := &bindingsv1.Binding{Name: "origin", Properties: &bindingsv1.Binding_Custom{Custom: custom}}
	for _, tc := range []struct {
		property string
		want     string
	}{
		{"certificate", "-----BEGIN CERTIFICATE-----\n"},
		{"port", "8443"},
		{"tls", "true"},
		{"zones", `["a","b"]`},
		{"limits", `{"rps":10}`},
	} {
		got, ok := VariableText(binding, tc.property)
		if !ok || got != tc.want {
			t.Errorf("VariableText(origin, %q) = %q, %v, want %q, true", tc.property, got, ok, tc.want)
		}
	}
}

func TestAVariableReadsNothingFromAPropertyACustomRecordDoesNotPublish(t *testing.T) {
	custom, err := structpb.NewStruct(map[string]any{"certificate": "pem"})
	if err != nil {
		t.Fatal(err)
	}
	binding := &bindingsv1.Binding{Name: "origin", Properties: &bindingsv1.Binding_Custom{Custom: custom}}
	if got, ok := VariableText(binding, "key"); ok {
		t.Errorf("VariableText(origin, key) = %q, true, want nothing", got)
	}
}

func TestAVariableReadsNothingFromATypedRecord(t *testing.T) {
	postgres := &bindingsv1.Binding{Name: "orders", Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{Host: "db"}}}
	if got, ok := VariableText(postgres, "host"); ok {
		t.Errorf("VariableText(orders, host) = %q, true, want nothing: only a custom record's properties are variable values", got)
	}
}
