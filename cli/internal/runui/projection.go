package runui

import (
	"encoding/hex"
	"fmt"
	"strings"
	"time"
	"unicode"

	"google.golang.org/protobuf/reflect/protoreflect"

	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

const (
	blockIndent    = "  "
	maxOrphanLines = 4096
)

type armFunc func(*projector, protoreflect.Message) []string

var overrides = map[protoreflect.FullName]armFunc{
	"common.progress.v1.StagePlanEvent": (*projector).stagePlan,
	"common.progress.v1.ProgressEvent":  (*projector).progress,
	"common.progress.v1.LogEvent":       (*projector).log,
	"common.progress.v1.SpanEvent":      (*projector).span,
	"common.progress.v1.DnsOwedEvent":   (*projector).dnsOwed,
	"common.progress.v1.DegradedEvent":  (*projector).degraded,
	"cli.stream.v1.RunResultEvent":      (*projector).result,
	"cli.stream.v1.DiagnosticEvent":     (*projector).diagnostic,
	"cli.stream.v1.WaitingEvent":        (*projector).waiting,
	"cli.stream.v1.ResumedEvent":        (*projector).resumed,
	"common.plan.v1.ChangePlan":         (*projector).plan,
}

type blockLine struct {
	text string
	raw  bool
}

type block struct {
	id    string
	path  string
	lines []blockLine
}

type projector struct {
	present Presentation
	tree    *stagePlan
	blocks  map[string]*block
	open    []string

	orphans    map[string][]blockLine
	pending    []string
	orphanLine int
}

func newProjector(present Presentation) *projector {
	return &projector{
		present: present,
		tree:    newStagePlan(),
		blocks:  make(map[string]*block),
		orphans: make(map[string][]blockLine),
	}
}

func (p *projector) project(ev *streamv1.RunEvent) []string {
	return p.render(ev.ProtoReflect())
}

func (p *projector) render(m protoreflect.Message) []string {
	if fn, ok := overrides[m.Descriptor().FullName()]; ok {
		return fn(p, m)
	}
	if sub, ok := setOneofMessage(m); ok {
		return p.render(sub)
	}
	return p.generated(m)
}

func setOneofMessage(m protoreflect.Message) (protoreflect.Message, bool) {
	oneofs := m.Descriptor().Oneofs()
	for i := 0; i < oneofs.Len(); i++ {
		od := oneofs.Get(i)
		if od.IsSynthetic() {
			continue
		}
		fd := m.WhichOneof(od)
		if fd == nil || fd.Kind() != protoreflect.MessageKind {
			continue
		}
		return m.Get(fd).Message(), true
	}
	return nil, false
}

func (p *projector) generated(m protoreflect.Message) []string {
	return append([]string{titleOf(m.Descriptor())}, fieldLines(m, blockIndent)...)
}

func titleOf(d protoreflect.Descriptor) string {
	name := strings.TrimSuffix(string(d.Name()), "Event")
	var b strings.Builder
	for i, r := range name {
		switch {
		case i == 0:
			b.WriteRune(unicode.ToUpper(r))
		case unicode.IsUpper(r):
			b.WriteRune(' ')
			b.WriteRune(unicode.ToLower(r))
		default:
			b.WriteRune(r)
		}
	}
	return b.String()
}

func fieldLines(m protoreflect.Message, indent string) []string {
	var out []string
	fields := m.Descriptor().Fields()
	for i := 0; i < fields.Len(); i++ {
		fd := fields.Get(i)
		if !m.Has(fd) {
			continue
		}
		out = append(out, valueLines(fd, m.Get(fd), indent)...)
	}
	return out
}

func valueLines(fd protoreflect.FieldDescriptor, v protoreflect.Value, indent string) []string {
	label := strings.ReplaceAll(string(fd.Name()), "_", " ")
	switch {
	case fd.IsList():
		list := v.List()
		if list.Len() == 0 {
			return nil
		}
		base := indent + blockIndent
		out := []string{indent + label + ":"}
		for i := 0; i < list.Len(); i++ {
			out = append(out, bulleted(listElement(fd, list.Get(i), base+blockIndent), base)...)
		}
		return out
	case fd.IsMap():
		var out []string
		keys := mapKeys(v.Map())
		for _, k := range keys {
			out = append(out, elementLines(fd.MapValue(), v.Map().Get(k), label+" "+k.String(), indent)...)
		}
		return out
	default:
		return elementLines(fd, v, label, indent)
	}
}

func mapKeys(m protoreflect.Map) []protoreflect.MapKey {
	var keys []protoreflect.MapKey
	m.Range(func(k protoreflect.MapKey, _ protoreflect.Value) bool {
		keys = append(keys, k)
		return true
	})
	for i := 1; i < len(keys); i++ {
		for j := i; j > 0 && keys[j].String() < keys[j-1].String(); j-- {
			keys[j], keys[j-1] = keys[j-1], keys[j]
		}
	}
	return keys
}

func listElement(fd protoreflect.FieldDescriptor, v protoreflect.Value, indent string) []string {
	if fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind {
		return fieldLines(v.Message(), indent)
	}
	return []string{indent + scalarText(fd, v)}
}

func bulleted(lines []string, base string) []string {
	if len(lines) == 0 {
		return nil
	}
	lines[0] = base + "- " + strings.TrimPrefix(lines[0], base+blockIndent)
	return lines
}

func elementLines(fd protoreflect.FieldDescriptor, v protoreflect.Value, label, indent string) []string {
	if fd.Kind() == protoreflect.MessageKind || fd.Kind() == protoreflect.GroupKind {
		nested := fieldLines(v.Message(), indent+blockIndent)
		if len(nested) == 0 {
			return nil
		}
		return append([]string{indent + label + ":"}, nested...)
	}
	text := scalarText(fd, v)
	if !strings.Contains(text, "\n") {
		return []string{fmt.Sprintf("%s%s: %s", indent, label, text)}
	}
	out := []string{indent + label + ":"}
	for _, line := range strings.Split(strings.TrimRight(text, "\n"), "\n") {
		out = append(out, indent+blockIndent+line)
	}
	return out
}

func scalarText(fd protoreflect.FieldDescriptor, v protoreflect.Value) string {
	switch fd.Kind() {
	case protoreflect.EnumKind:
		return enumText(fd.Enum(), v.Enum())
	case protoreflect.BytesKind:
		return hex.EncodeToString(v.Bytes())
	default:
		return v.String()
	}
}

func enumText(ed protoreflect.EnumDescriptor, n protoreflect.EnumNumber) string {
	value := ed.Values().ByNumber(n)
	if value == nil {
		return fmt.Sprint(int32(n))
	}
	name := string(value.Name())
	prefix := screamingSnake(string(ed.Name())) + "_"
	name = strings.TrimPrefix(name, prefix)
	return strings.ToLower(strings.ReplaceAll(name, "_", " "))
}

func screamingSnake(name string) string {
	var b strings.Builder
	for i, r := range name {
		if i > 0 && unicode.IsUpper(r) {
			b.WriteRune('_')
		}
		b.WriteRune(unicode.ToUpper(r))
	}
	return b.String()
}

func (p *projector) stagePlan(m protoreflect.Message) []string {
	ev := m.Interface().(*progressv1.StagePlanEvent)
	var out []string
	for _, s := range ev.GetStages() {
		p.tree.declare(s)
		id := stageKey(s.GetId())
		if id == "" || !p.isPhase(id) {
			continue
		}
		if _, exists := p.blocks[id]; exists {
			continue
		}
		b := &block{id: id, path: p.pathOf(id)}
		p.blocks[id] = b
		p.open = append(p.open, id)
		out = append(out, startMark+" "+b.path)
	}
	p.adopt()
	return out
}

func (p *projector) isPhase(id string) bool {
	n, ok := p.tree.nodes[id]
	if !ok || n.linkedParent == "" {
		return false
	}
	parent, ok := p.tree.nodes[n.linkedParent]
	return ok && parent.linkedParent == ""
}

func (p *projector) pathOf(id string) string {
	n, ok := p.tree.nodes[id]
	if !ok {
		return id
	}
	parent, ok := p.tree.nodes[n.linkedParent]
	if !ok {
		return n.title
	}
	return parent.title + " › " + n.title
}

func (p *projector) phaseOf(id string) *block {
	for depth := 0; depth < maxTreeDepth && id != ""; depth++ {
		if b, ok := p.blocks[id]; ok {
			return b
		}
		n, ok := p.tree.nodes[id]
		if !ok {
			return nil
		}
		id = n.linkedParent
	}
	return nil
}

func (p *projector) buffer(stageID []byte, text string, raw bool) []string {
	id := stageKey(stageID)
	lines := indented(text, raw)
	if len(lines) == 0 {
		return nil
	}
	if b := p.phaseOf(id); b != nil {
		b.lines = append(b.lines, lines...)
		return nil
	}
	if id != "" {
		p.hold(id, lines)
	}
	return nil
}

func indented(text string, raw bool) []blockLine {
	split := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	for len(split) > 0 && strings.TrimSpace(split[0]) == "" {
		split = split[1:]
	}
	for len(split) > 0 && strings.TrimSpace(split[len(split)-1]) == "" {
		split = split[:len(split)-1]
	}
	out := make([]blockLine, 0, len(split))
	for _, line := range split {
		out = append(out, blockLine{text: blockIndent + line, raw: raw})
	}
	return out
}

func (p *projector) hold(id string, lines []blockLine) {
	if p.orphanLine >= maxOrphanLines {
		return
	}
	if len(lines) > maxOrphanLines-p.orphanLine {
		lines = lines[:maxOrphanLines-p.orphanLine]
	}
	if _, held := p.orphans[id]; !held {
		p.pending = append(p.pending, id)
	}
	p.orphans[id] = append(p.orphans[id], lines...)
	p.orphanLine += len(lines)
}

func (p *projector) adopt() {
	var pending []string
	for _, id := range p.pending {
		b := p.phaseOf(id)
		if b == nil {
			pending = append(pending, id)
			continue
		}
		b.lines = append(b.lines, p.orphans[id]...)
		p.orphanLine -= len(p.orphans[id])
		delete(p.orphans, id)
	}
	p.pending = pending
}

func (p *projector) progress(m protoreflect.Message) []string {
	ev := m.Interface().(*progressv1.ProgressEvent)
	line := progressLogLine(ev.GetMessage(), ev.GetCurrent(), ev.Total)
	if line == "" {
		return nil
	}
	return p.buffer(ev.GetStageId(), line, false)
}

func (p *projector) log(m protoreflect.Message) []string {
	ev := m.Interface().(*progressv1.LogEvent)
	return p.buffer(ev.GetStageId(), ev.GetMessage(), true)
}

func (p *projector) span(m protoreflect.Message) []string {
	ev := m.Interface().(*progressv1.SpanEvent)
	id := stageKey(ev.GetSpanId())
	b, ok := p.blocks[id]
	if !ok {
		return nil
	}
	failed := ev.GetStatus() == progressv1.SpanStatus_SPAN_STATUS_ERROR
	return p.settle(b, spanDuration(ev), failed)
}

func spanDuration(ev *progressv1.SpanEvent) time.Duration {
	start, end := ev.GetStartTimeUnixNano(), ev.GetEndTimeUnixNano()
	if start <= 0 || end <= start {
		return 0
	}
	return time.Duration(end - start)
}

func (p *projector) take(b *block, keepRaw bool) []string {
	delete(p.blocks, b.id)
	for i, id := range p.open {
		if id == b.id {
			p.open = append(p.open[:i], p.open[i+1:]...)
			break
		}
	}
	out := make([]string, 0, len(b.lines))
	for _, line := range b.lines {
		if line.raw && !keepRaw {
			continue
		}
		out = append(out, line.text)
	}
	return out
}

func (p *projector) settle(b *block, d time.Duration, failed bool) []string {
	out := p.take(b, p.present.Verbose || failed)
	if failed {
		return append(out, fmt.Sprintf("%s %s failed  %s", failMark, b.path, formatDuration(d)))
	}
	return append(out, fmt.Sprintf("%s %s  %s", okMark, b.path, formatDuration(d)))
}

func (p *projector) strand(mark, note string) []string {
	var out []string
	for len(p.open) > 0 {
		b := p.blocks[p.open[0]]
		if b == nil {
			p.open = p.open[1:]
			continue
		}
		out = append(out, append(p.take(b, p.present.Verbose || mark == failMark), fmt.Sprintf("%s %s %s", mark, b.path, note))...)
	}
	return out
}

func (p *projector) dnsOwed(m protoreflect.Message) []string {
	ev := m.Interface().(*progressv1.DnsOwedEvent)
	records := ev.GetRecords()
	if len(records) == 0 {
		return nil
	}
	out := []string{"", warnMark + " " + dnsHeadline(ev.GetHeadline(), records), ""}
	head, rows := dnsRows(records, p.present.Width)
	if head == "" {
		out = append(out, dnsStack(records)...)
	} else {
		out = append(out, head)
		out = append(out, rows...)
	}
	notes := dnsNotes(records, ev.GetNotes())
	for i, note := range notes {
		if i == 0 {
			out = append(out, "")
		}
		out = append(out, dnsIndent+note)
	}
	return append(out, "")
}

func (p *projector) degraded(m protoreflect.Message) []string {
	ev := m.Interface().(*progressv1.DegradedEvent)
	return []string{fmt.Sprintf("%s %s: %s", warnMark, ev.GetNeed(), ev.GetDetail())}
}

func (p *projector) diagnostic(m protoreflect.Message) []string {
	ev := m.Interface().(*streamv1.DiagnosticEvent)
	if ev.GetMessage() == "" {
		return nil
	}
	lines := strings.Split(strings.TrimRight(ev.GetMessage(), "\n"), "\n")
	if ev.GetLevel() == streamv1.DiagnosticLevel_DIAGNOSTIC_LEVEL_WARNING {
		lines[0] = warnMark + " " + lines[0]
	}
	return lines
}

func (p *projector) waiting(m protoreflect.Message) []string {
	ev := m.Interface().(*streamv1.WaitingEvent)
	out := append(p.strand(warnMark, "paused"), "")
	out = append(out, strings.Split(strings.TrimRight(ev.GetReason(), "\n"), "\n")...)
	return append(out,
		"  Fill them in at:",
		"",
		"    "+ev.GetUrl(),
		"",
		"  Waiting for the page — press Ctrl-C to abort. Nothing has been provisioned.",
		"",
	)
}

func (p *projector) resumed(m protoreflect.Message) []string {
	ev := m.Interface().(*streamv1.ResumedEvent)
	return []string{okMark + " Resumed — " + ev.GetReason(), ""}
}

func (p *projector) result(m protoreflect.Message) []string {
	ev := m.Interface().(*streamv1.RunResultEvent)
	d := time.Duration(ev.GetDurationMs()) * time.Millisecond
	if ev.GetInterrupted() {
		out := p.strand(warnMark, "interrupted")
		out = append(out, "", fmt.Sprintf("%s %s in %s", warnMark, headlineOr(ev, "Interrupted"), formatDuration(d)))
		out = append(out, detailLines(ev.GetDetail())...)
		return append(out, logPointer("Log", ev.GetLogPath())...)
	}
	if ev.GetSuccess() {
		out := p.strand(warnMark, "unfinished")
		out = append(out, "", fmt.Sprintf("%s %s in %s", okMark, headlineOr(ev, "Done"), formatDuration(d)))
		switch {
		case len(ev.GetAppUrls()) > 0:
			out = append(out, "")
			for _, u := range ev.GetAppUrls() {
				out = append(out, blockIndent+u)
			}
		case ev.GetUrlNote() != "":
			out = append(out, "", blockIndent+ev.GetUrlNote())
		}
		if note := FlipNote(ev.GetFlipBound()); note != "" {
			out = append(out, "", blockIndent+note)
		}
		return append(out, logPointer("Details", ev.GetLogPath())...)
	}

	out := p.strand(failMark, "failed")
	out = append(out, "", failMark+" "+headlineOr(ev, "Failed"))
	out = append(out, detailLines(ev.GetDetail())...)
	return append(out, logPointer("Full log", ev.GetLogPath())...)
}

func headlineOr(ev *streamv1.RunResultEvent, fallback string) string {
	if h := ev.GetHeadline(); h != "" {
		return h
	}
	return fallback
}

func detailLines(detail string) []string {
	if detail == "" {
		return nil
	}
	var out []string
	for _, line := range strings.Split(strings.TrimRight(detail, "\n"), "\n") {
		out = append(out, blockIndent+line)
	}
	return out
}

func logPointer(label, logPath string) []string {
	if logPath == "" {
		return nil
	}
	return []string{"", fmt.Sprintf("%s%s: %s", blockIndent, label, relLog(logPath))}
}
