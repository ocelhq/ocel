package logs

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"strings"
	"unicode/utf8"

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
	raw      bool
	minLevel logview.Level
}

func newOutput(stdout io.Writer, asJSON, raw bool, minLevel logview.Level, warn func(string)) *output {
	var out format = plainFormat{stdout: stdout, warn: warn}
	if asJSON {
		out = jsonFormat{stdout: stdout}
	}
	return &output{format: out, raw: raw, minLevel: minLevel}
}

func (o *output) printResponse(resp *contractv1.ReadLogsResponse) error {
	switch body := resp.GetBody().(type) {
	case *contractv1.ReadLogsResponse_Batch:
		for _, entry := range body.Batch.GetEntries() {
			parsed := o.parse(entry.GetMessage())
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

func (o *output) parse(message string) logview.Entry {
	if o.raw {
		return logview.Entry{Message: message, Raw: message}
	}
	return logview.Parse(message)
}

type plainFormat struct {
	stdout io.Writer
	warn   func(string)
}

func (f plainFormat) writeEntry(entry *contractv1.LogEntry, parsed logview.Entry) error {
	app := terminal.SanitizeText(entry.GetApp())
	if source := terminal.SanitizeText(entry.GetSource()); source != "" {
		app += "/" + source
	}
	level := "-"
	if parsed.Level != logview.LevelUnknown {
		level = strings.ToUpper(parsed.Level.String())
	}
	parts := []string{entry.GetTime().AsTime().UTC().Format(timeLayout), app, level, plainMessage(parsed.Message)}
	if fields := plainFields(parsed); fields != "" {
		parts = append(parts, fields)
	}
	_, err := fmt.Fprintln(f.stdout, strings.Join(parts, " "))
	return err
}

func (f plainFormat) writeNotice(notice *contractv1.LogNotice) error {
	if notice.GetKind() != contractv1.LogNotice_KIND_CAUGHT_UP {
		f.warn(terminal.SanitizeText(notice.GetMessage()))
	}
	return nil
}

func plainMessage(message string) string {
	var lines []string
	for _, line := range strings.Split(message, "\n") {
		if text := terminal.SanitizeText(line); text != "" {
			lines = append(lines, text)
		}
	}
	return strings.Join(lines, `\n`)
}

func plainFields(parsed logview.Entry) string {
	fields := maps.Clone(parsed.Fields)
	if parsed.Error != "" {
		if fields == nil {
			fields = map[string]any{}
		}
		fields["error"] = parsed.Error
	}
	if len(fields) == 0 {
		return ""
	}
	encoded, err := encodeJSON(fields)
	if err != nil {
		return ""
	}
	return terminal.SanitizeText(encoded)
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
		Fields:   parsed.Fields,
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
	_, err = io.WriteString(f.stdout, escapeC1Controls(encoded)+"\n")
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

func escapeC1Controls(encoded string) string {
	var b strings.Builder
	for _, r := range encoded {
		if r >= 0x80 && r <= 0x9f && utf8.ValidRune(r) {
			fmt.Fprintf(&b, `\u%04x`, r)
			continue
		}
		b.WriteRune(r)
	}
	return b.String()
}
