package scope

import (
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
)

func IsProjectSlug(field protoreflect.FieldDescriptor) bool {
	return proto.GetExtension(field.Options(), scopev1.E_ProjectSlug).(bool)
}

func IsProjectTier(field protoreflect.FieldDescriptor) bool {
	return proto.GetExtension(field.Options(), scopev1.E_ProjectTier).(bool)
}

func ListProjectSlugs(message proto.Message) []string {
	var slugs []string
	rangeMarked(message.ProtoReflect(), IsProjectSlug, func(value protoreflect.Value) {
		slugs = append(slugs, value.String())
	})
	return slugs
}

func ListProjectTiers(message proto.Message) []environmentv1.Tier {
	var tiers []environmentv1.Tier
	rangeMarked(message.ProtoReflect(), IsProjectTier, func(value protoreflect.Value) {
		tiers = append(tiers, environmentv1.Tier(value.Enum()))
	})
	return tiers
}

func rangeMarked(message protoreflect.Message, marked func(protoreflect.FieldDescriptor) bool, each func(protoreflect.Value)) {
	message.Range(func(field protoreflect.FieldDescriptor, value protoreflect.Value) bool {
		switch {
		case marked(field):
			each(value)
		case field.Kind() != protoreflect.MessageKind || field.IsMap():
		case field.IsList():
			list := value.List()
			for i := range list.Len() {
				rangeMarked(list.Get(i).Message(), marked, each)
			}
		default:
			rangeMarked(value.Message(), marked, each)
		}
		return true
	})
}
