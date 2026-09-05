package edge

const ServeDescriptorFile = "serve.json"

const AppBundleFile = "edge/bundle.json"

type NeedDetail struct {
	Count    int      `json:"count"`
	Routes   []string `json:"routes,omitempty"`
	Matchers []string `json:"matchers,omitempty"`
}

type ServeDescriptor struct {
	Runtime     string              `json:"runtime"`
	BuildID     string              `json:"buildId"`
	EdgeRouting bool                `json:"edgeRouting"`
	Entry       string              `json:"entry"`
	Needs       map[Need]NeedDetail `json:"needs"`
}
