package boxapi

const (
	SocketDir  = "/run/ocel-live"
	SocketFile = "values.sock"
	SocketPath = SocketDir + "/" + SocketFile
	ValuesPath = "/values"
	SpacePath  = "/space"
)

type Answer struct {
	Values map[string]string `json:"values"`
}

type Space struct {
	Free  uint64 `json:"free"`
	Total uint64 `json:"total"`
}

const EnvSourceSyncCommand = "envsourcesync"
