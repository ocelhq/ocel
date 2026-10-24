package boxapi

import (
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

const (
	DockerCommand    = "docker"
	DockerPullAs     = "pull-as"
	DockerGrantsPath = TierRoot + "/docker.json"
	DockerConfigDir  = TierRoot + "/docker"

	ProxyNetwork = "ocel"
	TierLabel    = "ocel.tier"
)

type DockerBind struct {
	Path     string `json:"path"`
	Writable bool   `json:"writable"`
}

type DockerGrants struct {
	Binds      []DockerBind          `json:"binds"`
	Networks   []string              `json:"networks"`
	Containers []string              `json:"containers"`
	Commands   map[string][][]string `json:"commands,omitempty"`
}

func AppNetwork(tier environment.Tier, project string) string {
	return "ocel-" + naming.TierWord(tier) + "-" + naming.Sanitize(project)
}
