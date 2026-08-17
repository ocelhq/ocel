package edge

const ServeDescriptorFile = "serve.json"

const AppBundleFile = "edge/bundle.json"

type ServeDescriptor struct {
	Framework   string `json:"framework"`
	BuildID     string `json:"buildId"`
	EdgeRouting bool   `json:"edgeRouting"`
}
