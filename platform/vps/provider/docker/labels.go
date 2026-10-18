package docker

import (
	"strconv"

	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

const (
	LabelApp     = "ocel.app"
	LabelProject = "ocel.project"
	LabelRef     = "ocel.ref"
	LabelEnv     = "ocel.env"
	LabelTier    = boxapi.TierLabel

	LabelResource   = "ocel.resource"
	LabelGeneration = "ocel.generation"
	LabelOwner      = "ocel.database-owner"
)

func LabelSelector(label string) string {
	return "{{index .Config.Labels " + strconv.Quote(label) + "}}"
}

const (
	ContainerRestart  = "unless-stopped"
	ConfigLabel       = "ocel.config"
	ContainerCmdLabel = "ocel.command"
)

const (
	RestoredLabel = "ocel.restored"
	RestoredBy    = "deploy"
)
