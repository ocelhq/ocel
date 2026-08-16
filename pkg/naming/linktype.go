package naming

import (
	"maps"
	"slices"
	"strings"

	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
	"google.golang.org/protobuf/reflect/protoreflect"
)

const linkTypePrefix = "LINK_TYPE_"

var linkKinds = map[linksv1.LinkType]Kind{
	linksv1.LinkType_LINK_TYPE_POSTGRES: KindDatabase,
	linksv1.LinkType_LINK_TYPE_BUCKET:   KindBucket,
}

var membraneTypes = map[linksv1.LinkType]bool{
	linksv1.LinkType_LINK_TYPE_BUCKET: true,
}

func CrossesMembrane(t linksv1.LinkType) bool {
	return membraneTypes[t]
}

func LinkTypes() []linksv1.LinkType {
	return slices.Sorted(maps.Keys(linkKinds))
}

func KindOf(t linksv1.LinkType) (Kind, bool) {
	kind, ok := linkKinds[t]
	return kind, ok
}

func EnvFragment(t linksv1.LinkType) string {
	return strings.TrimPrefix(t.String(), linkTypePrefix)
}

func LinkTypeOf(l *linksv1.Link) linksv1.LinkType {
	switch l.GetProperties().(type) {
	case *linksv1.Link_Postgres:
		return linksv1.LinkType_LINK_TYPE_POSTGRES
	case *linksv1.Link_Bucket:
		return linksv1.LinkType_LINK_TYPE_BUCKET
	case *linksv1.Link_Custom:
		return linksv1.LinkType_LINK_TYPE_CUSTOM
	}
	return linksv1.LinkType_LINK_TYPE_UNSPECIFIED
}

func linkProperties(l *linksv1.Link) protoreflect.Message {
	m := l.ProtoReflect()
	fd := m.WhichOneof(m.Descriptor().Oneofs().ByName("properties"))
	if fd == nil {
		return nil
	}
	return m.Get(fd).Message()
}

func LinkProperty(l *linksv1.Link, name string) (any, bool) {
	if custom := l.GetCustom(); custom != nil {
		value, carries := custom.GetFields()[name]
		if !carries {
			return nil, false
		}
		return value.AsInterface(), true
	}
	properties := linkProperties(l)
	if properties == nil {
		return nil, false
	}
	fd := properties.Descriptor().Fields().ByJSONName(name)
	if fd == nil {
		return nil, false
	}
	return propertyValue(fd, properties.Get(fd)), true
}

func LinkPropertyNames(l *linksv1.Link) []string {
	if custom := l.GetCustom(); custom != nil {
		return slices.Sorted(maps.Keys(custom.GetFields()))
	}
	properties := linkProperties(l)
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
	}
	return v.String()
}
