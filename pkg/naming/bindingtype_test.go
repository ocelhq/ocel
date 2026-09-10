package naming

import (
	"reflect"
	"slices"
	"testing"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestKindOf(t *testing.T) {
	for _, tc := range []struct {
		typ  bindingsv1.BindingType
		want Kind
		ok   bool
	}{
		{bindingsv1.BindingType_BINDING_TYPE_POSTGRES, KindDatabase, true},
		{bindingsv1.BindingType_BINDING_TYPE_BUCKET, KindBucket, true},
		{bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED, "", false},
		{bindingsv1.BindingType(99), "", false},
	} {
		got, ok := KindOf(tc.typ)
		if ok != tc.ok || got != tc.want {
			t.Errorf("KindOf(%v) = %q, %v, want %q, %v", tc.typ, got, ok, tc.want, tc.ok)
		}
	}
}

func TestProxied(t *testing.T) {
	for _, tc := range []struct {
		typ  bindingsv1.BindingType
		want bool
	}{
		{bindingsv1.BindingType_BINDING_TYPE_BUCKET, true},
		{bindingsv1.BindingType_BINDING_TYPE_POSTGRES, false},
		{bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED, false},
		{bindingsv1.BindingType(99), false},
	} {
		if got := Proxied(tc.typ); got != tc.want {
			t.Errorf("Proxied(%v) = %v, want %v", tc.typ, got, tc.want)
		}
	}
}

func TestEnvFragment(t *testing.T) {
	for _, tc := range []struct {
		typ  bindingsv1.BindingType
		want string
	}{
		{bindingsv1.BindingType_BINDING_TYPE_POSTGRES, "POSTGRES"},
		{bindingsv1.BindingType_BINDING_TYPE_BUCKET, "BUCKET"},
	} {
		if got := EnvFragment(tc.typ); got != tc.want {
			t.Errorf("EnvFragment(%v) = %q, want %q", tc.typ, got, tc.want)
		}
	}
}

func TestResourceEnvName(t *testing.T) {
	for _, tc := range []struct {
		typ  bindingsv1.BindingType
		want string
	}{
		{bindingsv1.BindingType_BINDING_TYPE_POSTGRES, "OCEL_RESOURCE_POSTGRES_orders"},
		{bindingsv1.BindingType_BINDING_TYPE_BUCKET, "OCEL_RESOURCE_BUCKET_orders"},
	} {
		if got := ResourceEnvName(tc.typ, "orders"); got != tc.want {
			t.Errorf("ResourceEnvName(%v) = %q, want %q — the env contract does not move with the enum name", tc.typ, got, tc.want)
		}
	}
}

func TestEveryBindingTypeOcelProvisionsHasAKind(t *testing.T) {
	for name, value := range bindingsv1.BindingType_value {
		typ := bindingsv1.BindingType(value)
		if typ == bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED || typ == bindingsv1.BindingType_BINDING_TYPE_CUSTOM {
			continue
		}
		if _, ok := KindOf(typ); !ok {
			t.Errorf("%s has no naming kind", name)
		}
	}
	if _, ok := KindOf(bindingsv1.BindingType_BINDING_TYPE_CUSTOM); ok {
		t.Error("BINDING_TYPE_CUSTOM has a naming kind, which would let a resource declaration name one ocel never provisions")
	}
}

func TestBindingTypeOf(t *testing.T) {
	for _, tc := range []struct {
		binding *bindingsv1.Binding
		want    bindingsv1.BindingType
	}{
		{&bindingsv1.Binding{Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{}}}, bindingsv1.BindingType_BINDING_TYPE_POSTGRES},
		{&bindingsv1.Binding{Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{}}}, bindingsv1.BindingType_BINDING_TYPE_BUCKET},
		{&bindingsv1.Binding{Properties: &bindingsv1.Binding_Custom{Custom: &structpb.Struct{}}}, bindingsv1.BindingType_BINDING_TYPE_CUSTOM},
		{&bindingsv1.Binding{}, bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED},
		{nil, bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED},
	} {
		if got := BindingTypeOf(tc.binding); got != tc.want {
			t.Errorf("BindingTypeOf(%v) = %v, want %v", tc.binding, got, tc.want)
		}
	}
}

func TestBindingProperties(t *testing.T) {
	binding := &bindingsv1.Binding{Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{Host: "h", Port: 5433}}}
	if got := BindingPropertyNames(binding); !slices.Equal(got, []string{"database", "host", "password", "port", "username"}) {
		t.Errorf("BindingPropertyNames = %v", got)
	}
	if got, ok := BindingProperty(binding, "port"); !ok || got != float64(5433) {
		t.Errorf("BindingProperty(port) = %v, %v", got, ok)
	}
	if got, ok := BindingProperty(binding, "host"); !ok || got != "h" {
		t.Errorf("BindingProperty(host) = %v, %v", got, ok)
	}
	if _, ok := BindingProperty(binding, "bucket"); ok {
		t.Error("BindingProperty(bucket) found on a postgres binding")
	}
	if got := BindingPropertyNames(&bindingsv1.Binding{}); got != nil {
		t.Errorf("BindingPropertyNames(typeless) = %v, want nil", got)
	}
}

func TestCustomBindingProperties(t *testing.T) {
	custom, err := structpb.NewStruct(map[string]any{
		"subnetIds": []any{"subnet-a", "subnet-b"},
		"vpcId":     "vpc-1",
		"attached":  true,
		"maxConns":  float64(20),
	})
	if err != nil {
		t.Fatalf("build the published struct: %v", err)
	}
	binding := &bindingsv1.Binding{Properties: &bindingsv1.Binding_Custom{Custom: custom}}

	if got := BindingPropertyNames(binding); !slices.Equal(got, []string{"attached", "maxConns", "subnetIds", "vpcId"}) {
		t.Errorf("BindingPropertyNames = %v", got)
	}
	if got, ok := BindingProperty(binding, "subnetIds"); !ok || !reflect.DeepEqual(got, []any{"subnet-a", "subnet-b"}) {
		t.Errorf("BindingProperty(subnetIds) = %v, %v", got, ok)
	}
	for name, want := range map[string]any{"vpcId": "vpc-1", "attached": true, "maxConns": float64(20)} {
		if got, ok := BindingProperty(binding, name); !ok || got != want {
			t.Errorf("BindingProperty(%s) = %v, %v, want %v", name, got, ok, want)
		}
	}
	if _, ok := BindingProperty(binding, "host"); ok {
		t.Error("BindingProperty(host) found on a record that carries no such key")
	}
}
