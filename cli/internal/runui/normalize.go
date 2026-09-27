package runui

import (
	"fmt"
	"sort"

	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

func normalize(ev *streamv1.RunEvent) *streamv1.RunEvent {
	clone, ok := proto.Clone(ev).(*streamv1.RunEvent)
	if !ok {
		return ev
	}
	if clone.GetTime() == nil {
		clone.Time = timestamppb.Now()
	}
	if clone.GetLevel() == progressv1.Level_LEVEL_UNSPECIFIED {
		clone.Level = progressv1.Level_LEVEL_INFO
	}
	if ended := clone.GetEnded(); ended != nil && clone.GetTime().AsTime().UnixNano() < ended.GetStartTimeUnixNano() {
		clone.Time = timestamppb.Now()
	}
	normalizeMessage(clone.ProtoReflect())
	return clone
}

func normalizeMessage(m protoreflect.Message) {
	m.Range(func(fd protoreflect.FieldDescriptor, v protoreflect.Value) bool {
		switch {
		case fd.IsList():
			normalizeList(fd, v.List())
		case fd.IsMap():
			v.Map().Range(func(_ protoreflect.MapKey, mv protoreflect.Value) bool {
				normalizeValue(fd.MapValue(), mv, func(protoreflect.Value) {})
				return true
			})
		default:
			normalizeValue(fd, v, func(nv protoreflect.Value) { m.Set(fd, nv) })
		}
		return true
	})
	switch v := m.Interface().(type) {
	case *planv1.ChangePlan:
		sortGroups(v)
	case *planv1.ChangeGroup:
		sortChanges(v)
	}
}

func normalizeList(fd protoreflect.FieldDescriptor, list protoreflect.List) {
	for i := 0; i < list.Len(); i++ {
		idx := i
		normalizeValue(fd, list.Get(i), func(nv protoreflect.Value) { list.Set(idx, nv) })
	}
}

func normalizeValue(fd protoreflect.FieldDescriptor, v protoreflect.Value, set func(protoreflect.Value)) {
	switch fd.Kind() {
	case protoreflect.MessageKind, protoreflect.GroupKind:
		normalizeMessage(v.Message())
	case protoreflect.StringKind:
		if collapsed := collapseRewrites(v.String()); collapsed != v.String() {
			set(protoreflect.ValueOfString(collapsed))
		}
	}
}

func sortGroups(plan *planv1.ChangePlan) {
	sort.SliceStable(plan.Groups, func(i, j int) bool {
		return spineRank(plan.Groups[i].GetKind()) < spineRank(plan.Groups[j].GetKind())
	})
}

func sortChanges(group *planv1.ChangeGroup) {
	sort.SliceStable(group.Changes, func(i, j int) bool {
		return changeKey(group.Changes[i]) < changeKey(group.Changes[j])
	})
}

func changeKey(c *planv1.Change) string {
	return fmt.Sprintf("%s\x00%s\x00%d", c.GetKind(), c.GetName(), c.GetAction())
}
