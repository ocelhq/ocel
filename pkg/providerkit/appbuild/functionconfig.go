package appbuild

const FunctionConfigFile = "config.json"

type FunctionConfig struct {
	Framework Framework `json:"framework"`
	Handler   string    `json:"handler"`
	Command   []string  `json:"command,omitempty"`
	ID        string    `json:"id"`
	App       string    `json:"app"`
}
