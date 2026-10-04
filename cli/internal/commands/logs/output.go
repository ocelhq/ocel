package logs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"strings"

	"google.golang.org/protobuf/proto"

	"github.com/ocelhq/ocel/cli/internal/logview"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

const timeLayout = "2006-01-02T15:04:05.000Z07:00"

type format interface {
	writeEntry(entry *contractv1.LogEntry, parsed logview.Entry) error
	writeNotice(notice *contractv1.LogNotice) error
}

type output struct {
	format
	raw         bool
	joinsTraces bool
	minLevel    logview.Level
}

const liveMarker = "── live ──"

type notices struct {
	warn func(string)
	note func(string)
}

func (n notices) report(notice *contractv1.LogNotice) {
	message := terminal.SanitizeLogText(notice.GetMessage())
	switch notice.GetKind() {
	case contractv1.LogNotice_KIND_CAUGHT_UP:
	case contractv1.LogNotice_KIND_SAMPLED, contractv1.LogNotice_KIND_RECONNECTED:
		n.note(message)
	default:
		n.warn(message)
	}
}

func newOutput(stdout io.Writer, present terminal.Presentation, asJSON, raw bool, minLevel logview.Level, tail bool, notes notices) *output {
	out := &output{format: plainFormat{stdout: stdout, notes: notes}, raw: raw, minLevel: minLevel}
	switch {
	case asJSON:
		out.format = jsonFormat{stdout: stdout}
	case present.TTY && !raw:
		out.format = terminalFormat{view: logview.NewTerminalView(stdout, present), stdout: stdout, palette: present.Palette(), tail: tail, notes: notes}
		out.joinsTraces = true
	}
	return out
}

func (o *output) printResponse(resp *contractv1.ReadLogsResponse) error {
	switch body := resp.GetBody().(type) {
	case *contractv1.ReadLogsResponse_Batch:
		for _, group := range o.groupEntries(body.Batch.GetEntries()) {
			entry, parsed := o.mergeGroup(group)
			if parsed.Level < o.minLevel {
				continue
			}
			if err := o.writeEntry(entry, parsed); err != nil {
				return err
			}
		}
	case *contractv1.ReadLogsResponse_Notice:
		return o.writeNotice(body.Notice)
	}
	return nil
}

func (o *output) groupEntries(entries []*contractv1.LogEntry) [][]*contractv1.LogEntry {
	if o.joinsTraces {
		return groupTraces(entries)
	}
	groups := make([][]*contractv1.LogEntry, len(entries))
	for i := range entries {
		groups[i] = entries[i : i+1]
	}
	return groups
}

func (o *output) mergeGroup(group []*contractv1.LogEntry) (*contractv1.LogEntry, logview.Entry) {
	entry := group[0]
	parsed := o.parse(entry.GetMessage())
	if len(group) > 1 {
		entry = proto.CloneOf(entry)
		for _, next := range group[1:] {
			parsed.Message += "\n" + next.GetMessage()
			entry.Failure = entry.GetFailure() || next.GetFailure()
		}
	}
	parsed.Level = logview.ResolveLevel(parsed.Level, entry.GetSeverity(), entry.GetFailure())
	return entry, parsed
}

func (o *output) parse(message string) logview.Entry {
	if o.raw {
		return logview.Entry{Message: message, Raw: message}
	}
	return logview.Parse(message)
}

type plainFormat struct {
	stdout io.Writer
	notes  notices
}

func (f plainFormat) writeEntry(entry *contractv1.LogEntry, parsed logview.Entry) error {
	app := terminal.SanitizeLogText(entry.GetApp())
	if source := terminal.SanitizeLogText(entry.GetSource()); source != "" {
		app += "/" + source
	}
	level := "-"
	if parsed.Level != logview.LevelUnknown {
		level = strings.ToUpper(parsed.Level.String())
	}
	parts := []string{entry.GetTime().AsTime().UTC().Format(timeLayout), app, level, terminal.SanitizeLogText(parsed.Message)}
	if fields := plainFields(parsed); fields != "" {
		parts = append(parts, fields)
	}
	_, err := fmt.Fprintln(f.stdout, strings.Join(parts, " "))
	return err
}

func (f plainFormat) writeNotice(notice *contractv1.LogNotice) error {
	f.notes.report(notice)
	return nil
}

type terminalFormat struct {
	view    *logview.TerminalView
	stdout  io.Writer
	palette terminal.Palette
	tail    bool
	notes   notices
}

func (f terminalFormat) writeEntry(entry *contractv1.LogEntry, parsed logview.Entry) error {
	return f.view.Write(logview.TerminalLine{
		Time:    entry.GetTime().AsTime(),
		App:     entry.GetApp(),
		Source:  entry.GetSource(),
		Failure: entry.GetFailure(),
		Entry:   parsed,
	})
}

func (f terminalFormat) writeNotice(notice *contractv1.LogNotice) error {
	if f.tail && notice.GetKind() == contractv1.LogNotice_KIND_CAUGHT_UP {
		_, err := fmt.Fprintln(f.stdout, f.palette.Muted(liveMarker))
		return err
	}
	f.notes.report(notice)
	return nil
}

func fieldsWithError(parsed logview.Entry) map[string]any {
	if parsed.Error == "" {
		return parsed.Fields
	}
	fields := maps.Clone(parsed.Fields)
	if fields == nil {
		fields = map[string]any{}
	}
	fields["error"] = parsed.Error
	return fields
}

func plainFields(parsed logview.Entry) string {
	fields := fieldsWithError(parsed)
	if len(fields) == 0 {
		return ""
	}
	encoded, err := encodeJSON(fields)
	if err != nil {
		return ""
	}
	return terminal.EscapeControls(encoded)
}

type jsonFormat struct {
	stdout io.Writer
}

type entryObject struct {
	Type     string         `json:"type"`
	Time     string         `json:"time"`
	App      string         `json:"app"`
	Source   string         `json:"source"`
	Release  string         `json:"release"`
	Instance string         `json:"instance"`
	Stream   string         `json:"stream"`
	Level    string         `json:"level,omitempty"`
	Message  string         `json:"message"`
	Fields   map[string]any `json:"fields,omitempty"`
	Failure  bool           `json:"failure"`
}

type noticeObject struct {
	Type    string `json:"type"`
	Kind    string `json:"kind"`
	Message string `json:"message"`
	Omitted uint64 `json:"omitted"`
}

func (f jsonFormat) writeEntry(entry *contractv1.LogEntry, parsed logview.Entry) error {
	return f.write(entryObject{
		Type:     "entry",
		Time:     entry.GetTime().AsTime().UTC().Format(timeLayout),
		App:      entry.GetApp(),
		Source:   entry.GetSource(),
		Release:  entry.GetRelease(),
		Instance: entry.GetInstance(),
		Stream:   streamName(entry.GetStream()),
		Level:    parsed.Level.String(),
		Message:  entry.GetMessage(),
		Fields:   fieldsWithError(parsed),
		Failure:  entry.GetFailure(),
	})
}

func (f jsonFormat) writeNotice(notice *contractv1.LogNotice) error {
	return f.write(noticeObject{
		Type:    "notice",
		Kind:    strings.ToLower(strings.TrimPrefix(notice.GetKind().String(), "KIND_")),
		Message: notice.GetMessage(),
		Omitted: notice.GetOmitted(),
	})
}

func (f jsonFormat) write(object any) error {
	encoded, err := encodeJSON(object)
	if err != nil {
		return err
	}
	_, err = io.WriteString(f.stdout, terminal.EscapeControls(encoded)+"\n")
	return err
}

func streamName(stream contractv1.LogStream) string {
	if stream == contractv1.LogStream_LOG_STREAM_UNSPECIFIED {
		return ""
	}
	return strings.ToLower(strings.TrimPrefix(stream.String(), "LOG_STREAM_"))
}

func encodeJSON(value any) (string, error) {
	var buf bytes.Buffer
	encoder := json.NewEncoder(&buf)
	encoder.SetEscapeHTML(false)
	if err := encoder.Encode(value); err != nil {
		return "", err
	}
	return strings.TrimSuffix(buf.String(), "\n"), nil
}
