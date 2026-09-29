package run

import progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"

type PhaseDescription struct {
	Name             string
	Gerund           string
	ChangesResources bool
}

var phases = map[progressv1.Phase]PhaseDescription{
	progressv1.Phase_PHASE_CHECK:     {Name: "check", Gerund: "checking"},
	progressv1.Phase_PHASE_BUILD:     {Name: "build", Gerund: "building"},
	progressv1.Phase_PHASE_PLAN:      {Name: "plan", Gerund: "planning"},
	progressv1.Phase_PHASE_PROVISION: {Name: "provision", Gerund: "provisioning", ChangesResources: true},
	progressv1.Phase_PHASE_DEPLOY:    {Name: "deploy", Gerund: "deploying", ChangesResources: true},
	progressv1.Phase_PHASE_PROMOTE:   {Name: "promote", Gerund: "promoting", ChangesResources: true},
	progressv1.Phase_PHASE_DESTROY:   {Name: "destroy", Gerund: "destroying", ChangesResources: true},
}

func DescribePhase(phase progressv1.Phase) (PhaseDescription, bool) {
	description, ok := phases[phase]
	return description, ok
}
