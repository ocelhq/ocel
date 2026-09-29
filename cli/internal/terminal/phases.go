package terminal

import (
	"github.com/fatih/color"

	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
)

type phaseWords struct {
	name   string
	gerund string
	color  color.Attribute
}

var phases = map[progressv1.Phase]phaseWords{
	progressv1.Phase_PHASE_CHECK:     {name: "check", gerund: "checking", color: color.FgBlue},
	progressv1.Phase_PHASE_BUILD:     {name: "build", gerund: "building", color: color.FgMagenta},
	progressv1.Phase_PHASE_PLAN:      {name: "plan", gerund: "planning", color: color.FgHiCyan},
	progressv1.Phase_PHASE_PROVISION: {name: "provision", gerund: "provisioning", color: color.FgHiBlue},
	progressv1.Phase_PHASE_DEPLOY:    {name: "deploy", gerund: "deploying", color: color.FgHiMagenta},
	progressv1.Phase_PHASE_PROMOTE:   {name: "promote", gerund: "promoting", color: color.FgCyan},
	progressv1.Phase_PHASE_DESTROY:   {name: "destroy", gerund: "destroying", color: color.FgHiMagenta},
}

func phaseTag(present Presentation, phase progressv1.Phase) (string, bool) {
	words, ok := phases[phase]
	if !ok {
		return "", false
	}
	p := present.palette()
	return p.Muted("[") + p.paint(words.name, words.color) + p.Muted("]"), true
}
