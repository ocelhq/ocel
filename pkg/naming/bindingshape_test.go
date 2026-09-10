package naming

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

func TestBindingPropertyShapes(t *testing.T) {
	t.Run("an owned type is described by its descriptor, values or none", func(t *testing.T) {
		want := []PropertyShape{
			{Name: "host", JSONType: JSONTypeString},
			{Name: "port", JSONType: JSONTypeNumber},
			{Name: "database", JSONType: JSONTypeString},
			{Name: "username", JSONType: JSONTypeString},
			{Name: "password", JSONType: JSONTypeString},
		}
		empty := &bindingsv1.Binding{
			Name:       "orders",
			Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{}},
		}
		if got := BindingPropertyShapes(empty); !reflect.DeepEqual(got, want) {
			t.Errorf("BindingPropertyShapes(empty postgres) = %v, want %v", got, want)
		}

		filled := &bindingsv1.Binding{
			Name: "orders",
			Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
				Host: "db.internal", Port: 5432, Database: "app", Username: "app", Password: "pw",
			}},
		}
		if got := BindingPropertyShapes(filled); !reflect.DeepEqual(got, want) {
			t.Errorf("BindingPropertyShapes(filled postgres) = %v, want %v", got, want)
		}
	})

	t.Run("a custom record is described by the shape it carries", func(t *testing.T) {
		custom, err := structpb.NewStruct(map[string]any{
			"subnetIds":        []any{"subnet-0a1", "subnet-0b2"},
			"securityGroupIds": []any{"sg-1"},
			"port":             float64(5432),
			"public":           true,
			"tags":             map[string]any{"team": "core"},
			"empty":            []any{},
			"mixed":            []any{"a", float64(1)},
			"absent":           nil,
		})
		if err != nil {
			t.Fatalf("structpb.NewStruct: %v", err)
		}
		binding := &bindingsv1.Binding{Name: "network", Properties: &bindingsv1.Binding_Custom{Custom: custom}}

		want := []PropertyShape{
			{Name: "absent", JSONType: JSONTypeUnknown},
			{Name: "empty", JSONType: JSONTypeUnknown, List: true},
			{Name: "mixed", JSONType: JSONTypeUnknown, List: true},
			{Name: "port", JSONType: JSONTypeNumber},
			{Name: "public", JSONType: JSONTypeBoolean},
			{Name: "securityGroupIds", JSONType: JSONTypeString, List: true},
			{Name: "subnetIds", JSONType: JSONTypeString, List: true},
			{Name: "tags", JSONType: JSONTypeObject},
		}
		if got := BindingPropertyShapes(binding); !reflect.DeepEqual(got, want) {
			t.Errorf("BindingPropertyShapes(custom) = %v, want %v", got, want)
		}
	})

	t.Run("a record with no properties describes nothing", func(t *testing.T) {
		if got := BindingPropertyShapes(&bindingsv1.Binding{Name: "orders"}); got != nil {
			t.Errorf("BindingPropertyShapes(bare) = %v, want nil", got)
		}
	})
}
