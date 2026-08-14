package edge

const ServeDescriptorFile = "serve.json"

type ServeDescriptor struct {
	Framework   string `json:"framework"`
	BuildID     string `json:"buildId"`
	EdgeRouting bool   `json:"edgeRouting"`
}
