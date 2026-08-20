package naming

import (
	"reflect"
	"testing"

	"google.golang.org/protobuf/types/known/structpb"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
)

func TestLinkPropertyShapes(t *testing.T) {
	t.Run("an owned type is described by its descriptor, values or none", func(t *testing.T) {
		want := []PropertyShape{
			{Name: "host", JSONType: JSONTypeString},
			{Name: "port", JSONType: JSONTypeNumber},
			{Name: "database", JSONType: JSONTypeString},
			{Name: "username", JSONType: JSONTypeString},
			{Name: "password", JSONType: JSONTypeString},
		}
		empty := &linksv1.Link{
			Name:       "orders",
			Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{}},
		}
		if got := LinkPropertyShapes(empty); !reflect.DeepEqual(got, want) {
			t.Errorf("LinkPropertyShapes(empty postgres) = %v, want %v", got, want)
		}

		filled := &linksv1.Link{
			Name: "orders",
			Properties: &linksv1.Link_Postgres{Postgres: &linksv1.PostgresProperties{
				Host: "db.internal", Port: 5432, Database: "app", Username: "app", Password: "pw",
			}},
		}
		if got := LinkPropertyShapes(filled); !reflect.DeepEqual(got, want) {
			t.Errorf("LinkPropertyShapes(filled postgres) = %v, want %v", got, want)
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
		link := &linksv1.Link{Name: "network", Properties: &linksv1.Link_Custom{Custom: custom}}

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
		if got := LinkPropertyShapes(link); !reflect.DeepEqual(got, want) {
			t.Errorf("LinkPropertyShapes(custom) = %v, want %v", got, want)
		}
	})

	t.Run("a record with no properties describes nothing", func(t *testing.T) {
		if got := LinkPropertyShapes(&linksv1.Link{Name: "orders"}); got != nil {
			t.Errorf("LinkPropertyShapes(bare) = %v, want nil", got)
		}
	})
}
