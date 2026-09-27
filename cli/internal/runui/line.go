package runui

import (
	"strings"

	"github.com/fatih/color"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

const (
	okMark             = "✓"
	failMark           = "✗"
	continuationIndent = "      "
)

type line struct {
	level   progressv1.Level
	phase   progressv1.Phase
	subject string
	message string
	ends    progressv1.SpanStatus
}

type label struct {
	text  string
	attrs []color.Attribute
}

var levelLabels = map[progressv1.Level]label{
	progressv1.Level_LEVEL_DEBUG: {"DEBUG", []color.Attribute{color.Faint}},
	progressv1.Level_LEVEL_INFO:  {"INFO ", nil},
	progressv1.Level_LEVEL_WARN:  {"WARN ", []color.Attribute{color.FgYellow, color.Bold}},
	progressv1.Level_LEVEL_ERROR: {"ERROR", []color.Attribute{color.FgRed, color.Bold}},
}

var phaseNames = map[progressv1.Phase]string{
	progressv1.Phase_PHASE_CHECK:     "check",
	progressv1.Phase_PHASE_BUILD:     "build",
	progressv1.Phase_PHASE_PLAN:      "plan",
	progressv1.Phase_PHASE_PROVISION: "provision",
	progressv1.Phase_PHASE_DEPLOY:    "deploy",
	progressv1.Phase_PHASE_PROMOTE:   "promote",
	progressv1.Phase_PHASE_DESTROY:   "destroy",
}

var unitMarks = map[progressv1.SpanStatus]label{
	progressv1.SpanStatus_SPAN_STATUS_OK:    {okMark, []color.Attribute{color.FgGreen}},
	progressv1.SpanStatus_SPAN_STATUS_ERROR: {failMark, []color.Attribute{color.FgRed, color.Bold}},
}

func (l line) render(present Presentation) string {
	var b strings.Builder
	b.WriteString(levelLabels[l.level].render(present) + " ")
	if name, ok := phaseNames[l.phase]; ok {
		b.WriteString("[" + name + "] ")
	}
	if mark, ok := unitMarks[l.ends]; ok {
		b.WriteString(mark.render(present) + " ")
	}
	if l.subject != "" {
		b.WriteString(l.subject + ": ")
	}
	b.WriteString(strings.ReplaceAll(l.message, "\n", "\n"+continuationIndent))
	return b.String()
}

func (l line) annotation() string {
	text := l.message
	if l.subject != "" {
		text = l.subject + ": " + text
	}
	if name, ok := phaseNames[l.phase]; ok {
		text = "[" + name + "] " + text
	}
	return text
}

func (l label) render(present Presentation) string {
	if len(l.attrs) == 0 {
		return l.text
	}
	return colorFor(present, l.attrs...).Sprint(l.text)
}

func colorFor(present Presentation, attrs ...color.Attribute) *color.Color {
	c := color.New(attrs...)
	if present.Color {
		c.EnableColor()
	} else {
		c.DisableColor()
	}
	return c
}
