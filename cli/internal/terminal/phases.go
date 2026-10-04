package terminal

import (
	"github.com/fatih/color"

	"github.com/ocelhq/ocel/cli/internal/run"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

var phaseColors = map[progressv1.Phase]color.Attribute{
	progressv1.Phase_PHASE_CHECK:     color.FgBlue,
	progressv1.Phase_PHASE_BUILD:     color.FgMagenta,
	progressv1.Phase_PHASE_PLAN:      color.FgHiCyan,
	progressv1.Phase_PHASE_PROVISION: color.FgHiBlue,
	progressv1.Phase_PHASE_DEPLOY:    color.FgHiMagenta,
	progressv1.Phase_PHASE_PROMOTE:   color.FgCyan,
	progressv1.Phase_PHASE_DESTROY:   color.FgHiMagenta,
}

func phaseTag(present Presentation, phase progressv1.Phase) (string, bool) {
	description, ok := run.DescribePhase(phase)
	if !ok {
		return "", false
	}
	p := present.Palette()
	return p.Muted("[") + p.paint(description.Name, phaseColors[phase]) + p.Muted("]"), true
}
