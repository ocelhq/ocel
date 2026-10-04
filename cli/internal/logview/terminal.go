package logview

import (
	"fmt"
	"io"
	"maps"
	"slices"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"

	"github.com/ocelhq/ocel/cli/internal/terminal"
)

const (
	timeLayout       = "15:04:05.000"
	levelWidth       = 5
	maxValueColumns  = 80
	fieldIndent      = "    "
	nestedIndent     = "  "
	truncationMarker = "…"
	missingName      = "-"
	liveMarker       = "── live ──"
)

type TerminalLine struct {
	Time    time.Time
	App     string
	Source  string
	Failure bool
	Entry   Entry
}

type TerminalView struct {
	out      io.Writer
	palette  terminal.Palette
	verbose  bool
	maxWidth int
}

func NewTerminalView(out io.Writer, present terminal.Presentation) *TerminalView {
	view := &TerminalView{out: out, palette: present.Palette(), verbose: present.Verbose}
	if present.WidthMeasured {
		view.maxWidth = present.Width
	}
	return view
}

func (v *TerminalView) Write(line TerminalLine) error {
	var text string
	if line.Failure || line.Entry.Level == LevelError {
		text = v.formatHeader(line) + "\n" + v.formatBox(line.Entry) + "\n"
	} else {
		text = v.formatEntry(line)
	}
	_, err := io.WriteString(v.out, text)
	return err
}

func (v *TerminalView) WriteLiveMarker() error {
	_, err := io.WriteString(v.out, v.palette.Muted(liveMarker)+"\n")
	return err
}

func (v *TerminalView) formatHeader(line TerminalLine) string {
	app := terminal.SanitizeLogText(line.App)
	if app == "" {
		app = missingName
	}
	label := app
	if source := terminal.SanitizeLogText(line.Source); source != "" {
		label += "/" + source
	}
	parts := []string{
		v.palette.Muted(line.Time.Local().Format(timeLayout)),
		v.palette.Hue(app, label),
		v.formatLevel(line.Entry.Level),
	}
	return strings.Join(parts, "  ")
}

func (v *TerminalView) formatLevel(level Level) string {
	name := missingName
	if level != LevelUnknown {
		name = strings.ToUpper(level.String())
	}
	name = fmt.Sprintf("%-*s", levelWidth, name)
	switch level {
	case LevelError:
		return v.palette.Failure(name)
	case LevelWarn:
		return v.palette.Warning(name)
	case LevelDebug:
		return v.palette.Muted(name)
	}
	return name
}

func (v *TerminalView) formatEntry(line TerminalLine) string {
	entry := line.Entry
	messageLines := strings.Split(entry.Message, "\n")
	text := v.formatHeader(line) + "  " + terminal.SanitizeLogText(messageLines[0])
	inline := !v.verbose && !hasNestedField(entry.Fields)
	if inline && len(entry.Fields) > 0 {
		text += "  " + v.palette.Muted(v.formatInlineFields(entry.Fields))
	}
	for _, messageLine := range messageLines[1:] {
		text += "\n" + fieldIndent + terminal.SanitizeLogText(messageLine)
	}
	if !inline {
		for _, field := range v.formatFieldLines(entry.Fields) {
			text += "\n" + v.palette.Muted(fieldIndent+field)
		}
	}
	return text + "\n"
}

func (v *TerminalView) formatBox(entry Entry) string {
	messageLines := strings.Split(entry.Message, "\n")
	content := []string{terminal.SanitizeLogText(messageLines[0])}
	content = append(content, v.formatFieldLines(entry.Fields)...)
	stack := messageLines[1:]
	if entry.Error != "" {
		stack = append(stack, strings.Split(entry.Error, "\n")...)
	}
	if len(stack) > 0 {
		content = append(content, "")
		for _, line := range stack {
			content = append(content, v.palette.Muted(terminal.SanitizeLogText(line)))
		}
	}
	box := v.palette.FailureBox()
	text := strings.Join(content, "\n")
	if v.maxWidth > 0 && lipgloss.Width(text)+box.GetHorizontalFrameSize() > v.maxWidth {
		box = box.Width(v.maxWidth)
	}
	return box.Render(text)
}

func (v *TerminalView) formatInlineFields(fields map[string]any) string {
	pairs := make([]string, 0, len(fields))
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		pairs = append(pairs, quoteIfNeeded(terminal.SanitizeLogText(key))+"="+v.formatInlineValue(fields[key]))
	}
	return strings.Join(pairs, " ")
}

func (v *TerminalView) formatInlineValue(value any) string {
	if items, ok := value.([]any); ok {
		return v.formatList(items)
	}
	return quoteIfNeeded(v.formatScalar(value))
}

func (v *TerminalView) formatList(items []any) string {
	texts := make([]string, len(items))
	for i, item := range items {
		texts[i] = v.formatScalar(item)
	}
	return "[" + strings.Join(texts, ", ") + "]"
}

func (v *TerminalView) formatFieldLines(fields map[string]any) []string {
	var lines []string
	for _, key := range slices.Sorted(maps.Keys(fields)) {
		lines = v.appendValue(lines, "", terminal.SanitizeLogText(key)+":", fields[key])
	}
	return lines
}

func (v *TerminalView) appendValue(lines []string, indent, label string, value any) []string {
	switch value := value.(type) {
	case map[string]any:
		lines = append(lines, indent+label)
		for _, key := range slices.Sorted(maps.Keys(value)) {
			lines = v.appendValue(lines, indent+nestedIndent, terminal.SanitizeLogText(key)+":", value[key])
		}
		return lines
	case []any:
		if !hasNestedItem(value) {
			return append(lines, indent+label+" "+v.formatList(value))
		}
		lines = append(lines, indent+label)
		for _, item := range value {
			lines = v.appendItem(lines, indent+nestedIndent, item)
		}
		return lines
	}
	return append(lines, indent+label+" "+v.formatScalar(value))
}

func (v *TerminalView) appendItem(lines []string, indent string, item any) []string {
	object, ok := item.(map[string]any)
	if !ok {
		return v.appendValue(lines, indent, "-", item)
	}
	first := len(lines)
	for _, key := range slices.Sorted(maps.Keys(object)) {
		lines = v.appendValue(lines, indent+nestedIndent, terminal.SanitizeLogText(key)+":", object[key])
	}
	if first < len(lines) {
		lines[first] = indent + "- " + strings.TrimPrefix(lines[first], indent+nestedIndent)
	}
	return lines
}

func (v *TerminalView) formatScalar(value any) string {
	if value == nil {
		return "null"
	}
	text := terminal.SanitizeLogText(fmt.Sprint(value))
	if !v.verbose {
		return ansi.Truncate(text, maxValueColumns, truncationMarker)
	}
	return text
}

func quoteIfNeeded(value string) string {
	if value == "" || strings.ContainsAny(value, " \t=\"") {
		return `"` + strings.ReplaceAll(value, `"`, `\"`) + `"`
	}
	return value
}

func hasNestedField(fields map[string]any) bool {
	for _, value := range fields {
		if isNested(value) {
			return true
		}
	}
	return false
}

func hasNestedItem(items []any) bool {
	return slices.ContainsFunc(items, isNested)
}

func isNested(value any) bool {
	switch value := value.(type) {
	case map[string]any:
		return true
	case []any:
		return hasNestedItem(value)
	}
	return false
}
