package configdoc

import (
	"encoding/json"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
)

func bindingsSchema(t *testing.T) map[string]any {
	t.Helper()
	generated, err := Schema()
	if err != nil {
		t.Fatalf("schema: %v", err)
	}
	var schema struct {
		Properties map[string]json.RawMessage `json:"properties"`
	}
	if err := json.Unmarshal(generated, &schema); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	raw, held := schema.Properties["bindings"]
	if !held {
		t.Fatal("the schema has no bindings property")
	}
	var shape map[string]any
	if err := json.Unmarshal(raw, &shape); err != nil {
		t.Fatalf("unmarshal bindings: %v", err)
	}
	return shape
}

func TestBindingsSchemaKeysAreExactlyTheBindableTypes(t *testing.T) {
	shape := bindingsSchema(t)

	if shape["additionalProperties"] != false {
		t.Errorf("additionalProperties = %v, want false: an unknown type key must be caught in the editor", shape["additionalProperties"])
	}

	properties, ok := shape["properties"].(map[string]any)
	if !ok {
		t.Fatalf("bindings has no properties: %v", shape)
	}
	got := make([]string, 0, len(properties))
	for key := range properties {
		got = append(got, key)
	}
	slices.Sort(got)

	want := make([]string, 0, len(properties))
	for _, typ := range naming.BindableResourceTypes() {
		want = append(want, naming.ResourceTypeName(typ))
	}
	slices.Sort(want)

	if !slices.Equal(got, want) {
		t.Errorf("bindings keys = %v, want %v — the schema must not carry a second list of bindable types", got, want)
	}
}

func TestBindingsSchemaMapsADeclaredNameToAnExternalName(t *testing.T) {
	shape := bindingsSchema(t)
	properties := shape["properties"].(map[string]any)

	for key, raw := range properties {
		entry, ok := raw.(map[string]any)
		if !ok {
			t.Fatalf("bindings.%s = %v, want an object schema", key, raw)
		}
		if entry["type"] != "object" {
			t.Errorf("bindings.%s type = %v, want an object of declared name to external name", key, entry["type"])
		}
		values, ok := entry["additionalProperties"].(map[string]any)
		if !ok || values["type"] != "string" {
			t.Errorf("bindings.%s values = %v, want a string external name", key, entry["additionalProperties"])
		}
	}
}

func TestCheckRefusesAnUnbindableTypeKey(t *testing.T) {
	err := Check("", Document{}, map[string]any{
		"slug":     "shop",
		"bindings": map[string]any{"redis": map[string]any{"cache": "shared-redis"}},
	})
	if err == nil {
		t.Fatal("Check = nil, want redis refused: nothing declares a redis resource")
	}
	for _, want := range []string{"postgres", "bucket"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Check = %v, want it to name %q as a type that exists", err, want)
		}
	}
}

func TestCheckAdmitsABindingUnderEachBindableType(t *testing.T) {
	bindings := map[string]any{}
	for _, typ := range naming.BindableResourceTypes() {
		bindings[naming.ResourceTypeName(typ)] = map[string]any{"orders": "sst-orders"}
	}
	if err := Check("", Document{}, map[string]any{"slug": "shop", "bindings": bindings}); err != nil {
		t.Fatalf("Check = %v, want every bindable type admitted", err)
	}
}
