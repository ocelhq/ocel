package naming

import (
	"encoding/base64"
	"maps"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/processenv"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const bindingTypePrefix = "BINDING_TYPE_"

var bindingKinds = map[bindingsv1.BindingType]Kind{
	bindingsv1.BindingType_BINDING_TYPE_POSTGRES: KindDatabase,
	bindingsv1.BindingType_BINDING_TYPE_BUCKET:   KindBucket,
	bindingsv1.BindingType_BINDING_TYPE_TOPIC:    KindTopic,
	bindingsv1.BindingType_BINDING_TYPE_TASK:     KindTopic,
	bindingsv1.BindingType_BINDING_TYPE_KV:       KindKV,
	bindingsv1.BindingType_BINDING_TYPE_REALTIME: KindRealtime,
}

var proxiedTypes = map[bindingsv1.BindingType]bool{
	bindingsv1.BindingType_BINDING_TYPE_BUCKET:   true,
	bindingsv1.BindingType_BINDING_TYPE_TASK:     true,
	bindingsv1.BindingType_BINDING_TYPE_TOPIC:    true,
	bindingsv1.BindingType_BINDING_TYPE_REALTIME: true,
}

func Proxied(t bindingsv1.BindingType) bool {
	return proxiedTypes[t]
}

func BindingTypes() []bindingsv1.BindingType {
	return slices.Sorted(maps.Keys(bindingKinds))
}

func KindOf(t bindingsv1.BindingType) (Kind, bool) {
	kind, ok := bindingKinds[t]
	return kind, ok
}

func EnvFragment(t bindingsv1.BindingType) string {
	return strings.TrimPrefix(t.String(), bindingTypePrefix)
}

func BindingTypeOf(l *bindingsv1.Binding) bindingsv1.BindingType {
	switch l.GetProperties().(type) {
	case *bindingsv1.Binding_Postgres:
		return bindingsv1.BindingType_BINDING_TYPE_POSTGRES
	case *bindingsv1.Binding_Bucket:
		return bindingsv1.BindingType_BINDING_TYPE_BUCKET
	case *bindingsv1.Binding_Custom:
		return bindingsv1.BindingType_BINDING_TYPE_CUSTOM
	case *bindingsv1.Binding_Topic:
		return bindingsv1.BindingType_BINDING_TYPE_TOPIC
	case *bindingsv1.Binding_Task:
		return bindingsv1.BindingType_BINDING_TYPE_TASK
	case *bindingsv1.Binding_Kv:
		return bindingsv1.BindingType_BINDING_TYPE_KV
	case *bindingsv1.Binding_Realtime:
		return bindingsv1.BindingType_BINDING_TYPE_REALTIME
	}
	return bindingsv1.BindingType_BINDING_TYPE_UNSPECIFIED
}

func bindingProperties(l *bindingsv1.Binding) protoreflect.Message {
	m := l.ProtoReflect()
	fd := m.WhichOneof(m.Descriptor().Oneofs().ByName("properties"))
	if fd == nil {
		return nil
	}
	return m.Get(fd).Message()
}

func BindingProperty(l *bindingsv1.Binding, name string) (any, bool) {
	if custom := l.GetCustom(); custom != nil {
		value, ok := custom.GetFields()[name]
		if !ok {
			return nil, false
		}
		return value.AsInterface(), true
	}
	properties := bindingProperties(l)
	if properties == nil {
		return nil, false
	}
	fd := properties.Descriptor().Fields().ByJSONName(name)
	if fd == nil {
		return nil, false
	}
	return propertyValue(fd, properties.Get(fd)), true
}

func BindingPropertyNames(l *bindingsv1.Binding) []string {
	if custom := l.GetCustom(); custom != nil {
		return slices.Sorted(maps.Keys(custom.GetFields()))
	}
	properties := bindingProperties(l)
	if properties == nil {
		return nil
	}
	fields := properties.Descriptor().Fields()
	out := make([]string, 0, fields.Len())
	for i := range fields.Len() {
		out = append(out, fields.Get(i).JSONName())
	}
	slices.Sort(out)
	return out
}

func propertyValue(fd protoreflect.FieldDescriptor, v protoreflect.Value) any {
	if !fd.IsList() {
		return propertyScalar(fd, v)
	}
	list := v.List()
	out := make([]any, list.Len())
	for i := range out {
		out[i] = propertyScalar(fd, list.Get(i))
	}
	return out
}

func propertyScalar(fd protoreflect.FieldDescriptor, v protoreflect.Value) any {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return v.Bool()
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind:
		return float64(v.Int())
	case protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.Uint64Kind, protoreflect.Fixed64Kind:
		return float64(v.Uint())
	case protoreflect.FloatKind, protoreflect.DoubleKind:
		return v.Float()
	case protoreflect.BytesKind:
		return base64.StdEncoding.EncodeToString(v.Bytes())
	case protoreflect.EnumKind:
		if named := fd.Enum().Values().ByNumber(v.Enum()); named != nil {
			return string(named.Name())
		}
		return float64(v.Enum())
	}
	return v.String()
}

func ResourceEnvName(t bindingsv1.BindingType, resource string) string {
	return processenv.ResourceEnvVarPrefix + EnvFragment(t) + "_" + resource
}
