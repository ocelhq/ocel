package binding

import (
	"strings"
	"testing"

	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
)

func TestEveryCaseOfABindingsPropertiesNamesItsBindingType(t *testing.T) {
	t.Parallel()
	fields := (&bindingsv1.Binding{}).ProtoReflect().Descriptor().Oneofs().ByName("properties").Fields()
	if fields.Len() == 0 {
		t.Fatal("the binding record's properties oneof has no case")
	}
	for i := range fields.Len() {
		name := "BINDING_TYPE_" + strings.ToUpper(string(fields.Get(i).Name()))
		if _, ok := bindingsv1.BindingType_value[name]; !ok {
			t.Errorf("case %s names no binding type %s", fields.Get(i).Name(), name)
		}
	}
}

func TestABindingsTypeIsItsPropertiesCase(t *testing.T) {
	t.Parallel()
	cases := map[bindingsv1.BindingType]*bindingsv1.Binding{
		bindingsv1.BindingType_BINDING_TYPE_POSTGRES:    {Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{}}},
		bindingsv1.BindingType_BINDING_TYPE_KV:          {Properties: &bindingsv1.Binding_Kv{Kv: &bindingsv1.KvProperties{}}},
		bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED: {},
	}
	for want, record := range cases {
		if got := typeOf(record); got != want {
			t.Errorf("typeOf(%v) = %v, want %v", record, got, want)
		}
	}
}
