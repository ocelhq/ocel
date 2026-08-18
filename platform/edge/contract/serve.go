package edge

const ServeDescriptorFile = "serve.json"

const AppBundleFile = "edge/bundle.json"

type NeedDetail struct {
	Count    int      `json:"count"`
	Routes   []string `json:"routes,omitempty"`
	Matchers []string `json:"matchers,omitempty"`
}

type ServeDescriptor struct {
	Framework   string              `json:"framework"`
	BuildID     string              `json:"buildId"`
	EdgeRouting bool                `json:"edgeRouting"`
	Needs       map[Need]NeedDetail `json:"needs"`
}
