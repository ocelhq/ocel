package live

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
	Binds      []DockerBind `json:"binds"`
	Networks   []string     `json:"networks"`
	Containers []string     `json:"containers"`
}
