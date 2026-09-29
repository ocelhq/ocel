package buildoutput

const FunctionDescriptorFile = "config.json"

type FunctionDescriptor struct {
	Framework Framework `json:"framework"`
	EntryFile string    `json:"entryFile"`
	Command   []string  `json:"command,omitempty"`
	ID        string    `json:"id"`
	App       string    `json:"app"`
}
