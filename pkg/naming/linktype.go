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

func LinkProperty(l *linksv1.Link, name string) (string, bool) {
	properties := linkProperties(l)
	if properties == nil {
		return "", false
	}
	fd := properties.Descriptor().Fields().ByJSONName(name)
	if fd == nil {
		return "", false
	}
	return properties.Get(fd).String(), true
}

func LinkPropertyNames(l *linksv1.Link) []string {
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
