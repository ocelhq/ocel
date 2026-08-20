package naming

import (
	"maps"
	"slices"

	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/structpb"

	envvarsv1 "github.com/ocelhq/ocel/pkg/proto/envvars/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
)

const (
	JSONTypeString  = "string"
	JSONTypeNumber  = "number"
	JSONTypeBoolean = "boolean"
	JSONTypeObject  = "object"
	JSONTypeUnknown = "unknown"
)

type PropertyShape struct {
	Name     string `json:"name"`
	JSONType string `json:"jsonType"`
	List     bool   `json:"list,omitempty"`
}

func PropertyShapeMessages(shapes []PropertyShape) []*envvarsv1.PropertyShape {
	out := make([]*envvarsv1.PropertyShape, 0, len(shapes))
	for _, s := range shapes {
		out = append(out, &envvarsv1.PropertyShape{Name: s.Name, JsonType: s.JSONType, List: s.List})
	}
	return out
}

func LinkPropertyShapes(l *linksv1.Link) []PropertyShape {
	if custom := l.GetCustom(); custom != nil {
		fields := custom.GetFields()
		out := make([]PropertyShape, 0, len(fields))
		for _, name := range slices.Sorted(maps.Keys(fields)) {
			out = append(out, structShape(name, fields[name]))
		}
		return out
	}

	properties := linkProperties(l)
	if properties == nil {
		return nil
	}
	fields := properties.Descriptor().Fields()
	out := make([]PropertyShape, 0, fields.Len())
	for i := range fields.Len() {
		fd := fields.Get(i)
		out = append(out, PropertyShape{
			Name:     fd.JSONName(),
			JSONType: fieldJSONType(fd),
			List:     fd.IsList(),
		})
	}
	return out
}

func fieldJSONType(fd protoreflect.FieldDescriptor) string {
	switch fd.Kind() {
	case protoreflect.BoolKind:
		return JSONTypeBoolean
	case protoreflect.Int32Kind, protoreflect.Sint32Kind, protoreflect.Sfixed32Kind,
		protoreflect.Int64Kind, protoreflect.Sint64Kind, protoreflect.Sfixed64Kind,
		protoreflect.Uint32Kind, protoreflect.Fixed32Kind, protoreflect.Uint64Kind, protoreflect.Fixed64Kind,
		protoreflect.FloatKind, protoreflect.DoubleKind:
		return JSONTypeNumber
	case protoreflect.StringKind, protoreflect.BytesKind, protoreflect.EnumKind:
		return JSONTypeString
	case protoreflect.MessageKind, protoreflect.GroupKind:
		return JSONTypeObject
	}
	return JSONTypeUnknown
}

func structShape(name string, value *structpb.Value) PropertyShape {
	if list, isList := value.GetKind().(*structpb.Value_ListValue); isList {
		return PropertyShape{Name: name, JSONType: elementJSONType(list.ListValue.GetValues()), List: true}
	}
	return PropertyShape{Name: name, JSONType: valueJSONType(value)}
}

func elementJSONType(values []*structpb.Value) string {
	if len(values) == 0 {
		return JSONTypeUnknown
	}
	first := valueJSONType(values[0])
	for _, v := range values[1:] {
		if valueJSONType(v) != first {
			return JSONTypeUnknown
		}
	}
	return first
}

func valueJSONType(value *structpb.Value) string {
	switch value.GetKind().(type) {
	case *structpb.Value_StringValue:
		return JSONTypeString
	case *structpb.Value_NumberValue:
		return JSONTypeNumber
	case *structpb.Value_BoolValue:
		return JSONTypeBoolean
	case *structpb.Value_StructValue:
		return JSONTypeObject
	}
	return JSONTypeUnknown
}
