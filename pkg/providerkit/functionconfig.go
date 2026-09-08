package providerkit

const FunctionConfigFile = "config.json"

type FunctionConfig struct {
	Runtime Runtime  `json:"runtime"`
	Handler string   `json:"handler"`
	Command []string `json:"command,omitempty"`
	ID      string   `json:"id"`
	App     string   `json:"app"`
}
