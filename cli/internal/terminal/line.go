package terminal

import (
	"strings"

	"github.com/ocelhq/ocel/cli/internal/run"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

const (
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
	dim     bool
	status  progressv1.SpanStatus
	header  bool
}

func (l line) render(present Presentation) string {
	var b strings.Builder
	b.WriteString(levelLabels[l.level].render(present) + " ")
	if tag, ok := phaseTag(present, l.phase); ok {
		b.WriteString(tag + " ")
	}
	if mark, ok := spanMarks[l.status]; ok {
		b.WriteString(mark.render(present) + " ")
	}
	if l.subject != "" {
		b.WriteString(present.palette().Bold(l.subject) + ": ")
	}
	indent := continuationIndent
	if l.header {
		indent = headerHangIndent
	}
	message := l.message
	if l.dim {
		message = present.palette().Muted(message)
	}
	b.WriteString(strings.ReplaceAll(message+present.palette().Muted(l.timing)+l.outcome, "\n", "\n"+indent))
	return b.String()
}

func (l line) annotation() string {
	text := l.message + l.timing + l.outcome
	if l.subject != "" {
		text = l.subject + ": " + text
	}
	if described, ok := run.DescribePhase(l.phase); ok {
		text = "[" + described.Name + "] " + text
	}
	return text
}
