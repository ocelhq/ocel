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
	headerHangIndent   = "        "
)

type line struct {
	level   progressv1.Level
	phase   progressv1.Phase
	subject string
	message string
	timing  string
	outcome string
	quiet   bool
	ends    progressv1.SpanStatus
	heads   bool
}

type label struct {
	text  string
	attrs []color.Attribute
}

var levelLabels = map[progressv1.Level]label{
	progressv1.Level_LEVEL_DEBUG: {"DEBUG", []color.Attribute{color.Faint, color.FgHiBlack}},
	progressv1.Level_LEVEL_INFO:  {"INFO ", []color.Attribute{color.FgHiBlack}},
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

var phaseColors = map[progressv1.Phase]color.Attribute{
	progressv1.Phase_PHASE_CHECK:     color.FgBlue,
	progressv1.Phase_PHASE_BUILD:     color.FgMagenta,
	progressv1.Phase_PHASE_PLAN:      color.FgHiCyan,
	progressv1.Phase_PHASE_PROVISION: color.FgHiBlue,
	progressv1.Phase_PHASE_DEPLOY:    color.FgHiMagenta,
	progressv1.Phase_PHASE_PROMOTE:   color.FgCyan,
	progressv1.Phase_PHASE_DESTROY:   color.FgHiMagenta,
}

var unitMarks = map[progressv1.SpanStatus]label{
	progressv1.SpanStatus_SPAN_STATUS_OK:    {okMark, []color.Attribute{color.FgGreen}},
	progressv1.SpanStatus_SPAN_STATUS_ERROR: {failMark, []color.Attribute{color.FgRed, color.Bold}},
}

func (l line) render(present Presentation) string {
	var b strings.Builder
	b.WriteString(levelLabels[l.level].render(present) + " ")
	if tag, ok := phaseTag(present, l.phase); ok {
		b.WriteString(tag + " ")
	}
	if mark, ok := unitMarks[l.ends]; ok {
		b.WriteString(mark.render(present) + " ")
	}
	if l.subject != "" {
		b.WriteString(colorFor(present, color.Bold).Sprint(l.subject) + ": ")
	}
	indent := continuationIndent
	if l.heads {
		indent = headerHangIndent
	}
	message := l.message
	if l.quiet {
		message = muted(present, message)
	}
	b.WriteString(strings.ReplaceAll(message+muted(present, l.timing)+l.outcome, "\n", "\n"+indent))
	return b.String()
}

func (l line) annotation() string {
	text := l.message + l.timing + l.outcome
	if l.subject != "" {
		text = l.subject + ": " + text
	}
	if name, ok := phaseNames[l.phase]; ok {
		text = "[" + name + "] " + text
	}
	return text
}

func phaseTag(present Presentation, phase progressv1.Phase) (string, bool) {
	name, ok := phaseNames[phase]
	if !ok {
		return "", false
	}
	return muted(present, "[") + colorFor(present, phaseColors[phase]).Sprint(name) + muted(present, "]"), true
}

func muted(present Presentation, text string) string {
	return paintLines(colorFor(present, color.FgHiBlack), text)
}

func paintLines(c *color.Color, text string) string {
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		if l != "" {
			lines[i] = c.Sprint(l)
		}
	}
	return strings.Join(lines, "\n")
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
