package bindings

import (
	"reflect"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"google.golang.org/protobuf/types/known/structpb"
)

func TestABindingTypeOcelProvisionsNamesTheResourceKindItProvisions(t *testing.T) {
	for _, tc := range []struct {
		typ  bindingsv1.BindingType
		want naming.Kind
		ok   bool
	}{
		{bindingsv1.BindingType_BINDING_TYPE_POSTGRES, naming.KindDatabase, true},
		{bindingsv1.BindingType_BINDING_TYPE_BUCKET, naming.KindBucket, true},
		{bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED, "", false},
		{bindingsv1.BindingType(99), "", false},
	} {
		got, ok := KindOf(tc.typ)
		if ok != tc.ok || got != tc.want {
			t.Errorf("KindOf(%v) = %q, %v, want %q, %v", tc.typ, got, ok, tc.want, tc.ok)
		}
	}
}

func TestAPostgresBindingIsNotReachedThroughTheRuntimeProxyWhileABucketIs(t *testing.T) {
	for _, tc := range []struct {
		typ  bindingsv1.BindingType
		want bool
	}{
		{bindingsv1.BindingType_BINDING_TYPE_BUCKET, true},
		{bindingsv1.BindingType_BINDING_TYPE_REALTIME, true},
		{bindingsv1.BindingType_BINDING_TYPE_POSTGRES, false},
		{bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED, false},
		{bindingsv1.BindingType(99), false},
	} {
		if got := Proxied(tc.typ); got != tc.want {
			t.Errorf("Proxied(%v) = %v, want %v", tc.typ, got, tc.want)
		}
	}
}

func TestABindingTypesEnvFragmentIsItsUpperCaseName(t *testing.T) {
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

func TestABindingsResourceEnvNameIsOcelResourceThenItsTypeThenItsName(t *testing.T) {
	for _, tc := range []struct {
		typ  bindingsv1.BindingType
		want string
	}{
		{bindingsv1.BindingType_BINDING_TYPE_POSTGRES, "OCEL_RESOURCE_POSTGRES_orders"},
		{bindingsv1.BindingType_BINDING_TYPE_BUCKET, "OCEL_RESOURCE_BUCKET_orders"},
		{bindingsv1.BindingType_BINDING_TYPE_TOPIC, "OCEL_RESOURCE_TOPIC_orders"},
		{bindingsv1.BindingType_BINDING_TYPE_TASK, "OCEL_RESOURCE_TASK_orders"},
		{bindingsv1.BindingType_BINDING_TYPE_KV, "OCEL_RESOURCE_KV_orders"},
		{bindingsv1.BindingType_BINDING_TYPE_REALTIME, "OCEL_RESOURCE_REALTIME_orders"},
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

func TestABindingsTypeIsTheKindOfItsProperties(t *testing.T) {
	for _, tc := range []struct {
		binding *bindingsv1.Binding
		want    bindingsv1.BindingType
	}{
		{&bindingsv1.Binding{Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{}}}, bindingsv1.BindingType_BINDING_TYPE_POSTGRES},
		{&bindingsv1.Binding{Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{}}}, bindingsv1.BindingType_BINDING_TYPE_BUCKET},
		{&bindingsv1.Binding{Properties: &bindingsv1.Binding_Custom{Custom: &structpb.Struct{}}}, bindingsv1.BindingType_BINDING_TYPE_CUSTOM},
		{&bindingsv1.Binding{Properties: &bindingsv1.Binding_Topic{Topic: &bindingsv1.TopicProperties{}}}, bindingsv1.BindingType_BINDING_TYPE_TOPIC},
		{&bindingsv1.Binding{Properties: &bindingsv1.Binding_Task{Task: &bindingsv1.TaskProperties{}}}, bindingsv1.BindingType_BINDING_TYPE_TASK},
		{&bindingsv1.Binding{Properties: &bindingsv1.Binding_Kv{Kv: &bindingsv1.KvProperties{}}}, bindingsv1.BindingType_BINDING_TYPE_KV},
		{&bindingsv1.Binding{Properties: &bindingsv1.Binding_Realtime{Realtime: &bindingsv1.RealtimeProperties{}}}, bindingsv1.BindingType_BINDING_TYPE_REALTIME},
		{&bindingsv1.Binding{}, bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED},
		{nil, bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED},
	} {
		if got := TypeOf(tc.binding); got != tc.want {
			t.Errorf("TypeOf(a binding whose properties are %T) = %v, want %v", tc.binding.GetProperties(), got, tc.want)
		}
	}
}

func TestABindingsPropertiesAreReadByTheirJSONNames(t *testing.T) {
	binding := &bindingsv1.Binding{Properties: &bindingsv1.Binding_Postgres{Postgres: &bindingsv1.PostgresProperties{
		Host: "h", Port: 5433, TlsMode: bindingsv1.PostgresTlsMode_POSTGRES_TLS_MODE_VERIFY_FULL,
	}}}
	if got := PropertyNames(binding); !slices.Equal(got, []string{"database", "host", "password", "port", "tlsCa", "tlsMode", "tlsServerName", "url", "username"}) {
		t.Errorf("PropertyNames = %v", got)
	}
	if got, ok := Property(binding, "port"); !ok || got != float64(5433) {
		t.Errorf("Property(port) = %v, %v", got, ok)
	}
	if got, ok := Property(binding, "tlsMode"); !ok || got != "POSTGRES_TLS_MODE_VERIFY_FULL" {
		t.Errorf("Property(tlsMode) = %v, %v, want the name the record's json uses", got, ok)
	}
	if got, ok := Property(binding, "host"); !ok || got != "h" {
		t.Errorf("Property(host) = %v, %v", got, ok)
	}
	if _, ok := Property(binding, "bucket"); ok {
		t.Error("Property(bucket) found on a postgres binding")
	}
	if got := PropertyNames(&bindingsv1.Binding{}); got != nil {
		t.Errorf("PropertyNames(typeless) = %v, want nil", got)
	}
}

func TestACustomBindingsPropertiesAreTheKeysOfItsPublishedStruct(t *testing.T) {
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

	if got := PropertyNames(binding); !slices.Equal(got, []string{"attached", "maxConns", "subnetIds", "vpcId"}) {
		t.Errorf("PropertyNames = %v", got)
	}
	if got, ok := Property(binding, "subnetIds"); !ok || !reflect.DeepEqual(got, []any{"subnet-a", "subnet-b"}) {
		t.Errorf("Property(subnetIds) = %v, %v", got, ok)
	}
	for name, want := range map[string]any{"vpcId": "vpc-1", "attached": true, "maxConns": float64(20)} {
		if got, ok := Property(binding, name); !ok || got != want {
			t.Errorf("Property(%s) = %v, %v, want %v", name, got, ok, want)
		}
	}
	if _, ok := Property(binding, "host"); ok {
		t.Error("Property(host) found on a record that has no such key")
	}
}

func TestATaskSharesItsNamespaceWithTopics(t *testing.T) {
	topic, _ := KindOf(bindingsv1.BindingType_BINDING_TYPE_TOPIC)
	task, _ := KindOf(bindingsv1.BindingType_BINDING_TYPE_TASK)
	if topic != naming.KindTopic || task != naming.KindTopic {
		t.Errorf("KindOf(topic) = %q, KindOf(task) = %q, want both %q: a task is a topic with one consumer, and the two names share one namespace", topic, task, naming.KindTopic)
	}
}

func TestAKVStoreIsAComponentOfItsOwn(t *testing.T) {
	kind, bound := KindOf(bindingsv1.BindingType_BINDING_TYPE_KV)
	if !bound || kind != naming.KindKV || kind.Component() != "kv" {
		t.Errorf("KindOf(kv) = %q (bound %v), component %q, want %q with component \"kv\"", kind, bound, kind.Component(), naming.KindKV)
	}
}

func TestARealtimeResourceIsAComponentOfItsOwn(t *testing.T) {
	kind, bound := KindOf(bindingsv1.BindingType_BINDING_TYPE_REALTIME)
	if !bound || kind != naming.KindRealtime || kind.Component() != "realtime" {
		t.Errorf("KindOf(realtime) = %q (bound %v), component %q, want %q with component \"realtime\"", kind, bound, kind.Component(), naming.KindRealtime)
	}
}
