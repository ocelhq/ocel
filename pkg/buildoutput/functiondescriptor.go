package buildoutput

const FunctionDescriptorFile = "config.json"

type FunctionDescriptor struct {
	Framework Framework `json:"framework"`
	EntryFile string    `json:"entryFile"`
	Command   []string  `json:"command,omitempty"`
	Worker    []string  `json:"worker,omitempty"`
	ID        string    `json:"id"`
	App       string    `json:"app"`
}
