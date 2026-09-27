package runui

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"sort"
	"strings"
	"sync"

	"google.golang.org/protobuf/encoding/protojson"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/reflect/protoreflect"
	"google.golang.org/protobuf/types/known/timestamppb"
	"google.golang.org/protobuf/types/known/wrapperspb"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	planv1 "github.com/ocelhq/ocel/pkg/proto/common/plan/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type Stream struct {
	present Presentation

	mu   sync.Mutex
	w    io.Writer
	proj *projector
	r    *Renderer
}

func NewStream(w io.Writer, present Presentation) *Stream {
	s := newStream(w, present)
	if s.r != nil {
		s.r.startTicking()
	}
	return s
}

func newStream(w io.Writer, present Presentation) *Stream {
	s := &Stream{present: present, w: w}
	if present.Format == FormatHuman {
		s.proj = newProjector(present)
		s.r = newRenderer(w, present)
	}
	return s
}

func (s *Stream) Suspend() func() {
	if s.r == nil {
		return func() {}
	}
	return s.r.Suspend()
}

func (s *Stream) Spin(message string) *Spinner {
	if s.r == nil {
		return &Spinner{}
	}
	return &Spinner{stopFn: s.r.Spin(message), suspendFn: s.r.Suspend}
}

func (s *Stream) Restart(stageID []byte) {
	if s.r != nil {
		s.r.Restart(stageID)
	}
}

func (s *Stream) Emit(ev *streamv1.RunEvent) *streamv1.RunEvent {
	ev = normalize(ev)
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.present.Format == FormatJSON {
		s.writeJSONLocked(ev)
		return ev
	}
	lines := s.proj.project(ev)
	if ev.GetWaiting() != nil {
		s.r.Pause()
	}
	s.r.Ingest(ev)
	s.r.Commit(lines)
	if ev.GetResumed() != nil {
		s.r.Resume()
	}
	return ev
}

func (s *Stream) Pause() {
	if s.r != nil {
		s.r.Pause()
	}
}

func (s *Stream) Resume() {
	if s.r != nil {
		s.r.Resume()
	}
}

func (s *Stream) Close() error {
	if s.r != nil {
		err := s.r.Close()
		fmt.Fprintln(s.w)
		return err
	}
	return nil
}

func (s *Stream) writeJSONLocked(ev *streamv1.RunEvent) {
	line, err := envelopeJSON(ev)
	if err != nil {
		return
	}
	fmt.Fprintln(s.w, line)
}

func envelopeJSON(ev *streamv1.RunEvent) (string, error) {
	body := proto.CloneOf(ev)
	body.Time, body.Level, body.Phase, body.Subject, body.Message = nil, 0, 0, "", ""
	fields := []proto.Message{
		ev.GetTime(),
		wrapperspb.String(ev.GetLevel().String()),
		wrapperspb.String(ev.GetPhase().String()),
		wrapperspb.String(ev.GetSubject()),
		wrapperspb.String(ev.GetMessage()),
	}
	parts := make([]string, 0, 6)
	for i, key := range []string{"time", "level", "phase", "subject", "message"} {
		value, err := protojson.Marshal(fields[i])
		if err != nil {
			return "", err
		}
		parts = append(parts, fmt.Sprintf("%q:%s", key, value))
	}
	rest, err := protojson.Marshal(body)
	if err != nil {
		return "", err
	}
	if inner := bytes.TrimSpace(bytes.TrimSuffix(bytes.TrimPrefix(bytes.TrimSpace(rest), []byte("{")), []byte("}"))); len(inner) > 0 {
		parts = append(parts, string(inner))
	}
	var stable bytes.Buffer
	if err := json.Compact(&stable, []byte("{"+strings.Join(parts, ",")+"}")); err != nil {
		return "", err
	}
	return stable.String(), nil
}

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
