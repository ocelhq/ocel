package router

import "github.com/ocelhq/ocel/pkg/edge"

type ReleaseRecord struct {
	App                  string            `json:"app"`
	Framework            string            `json:"framework"`
	Release              string            `json:"release"`
	BuildID              string            `json:"buildId"`
	Entry                string            `json:"entry"`
	EntryFunction        string            `json:"entryFunction,omitempty"`
	Image                string            `json:"image,omitempty"`
	Physical             string            `json:"physical,omitempty"`
	Revisions            map[string]string `json:"revisions,omitempty"`
	Origin               string            `json:"origin,omitempty"`
	HealthPath           string            `json:"healthPath,omitempty"`
	HealthPathDiscovered bool              `json:"healthPathDiscovered,omitempty"`
	RoutingManifest      any               `json:"routingManifest"`
	FunctionURLs         map[string]string `json:"functionUrls"`
	AssetPrefix          string            `json:"assetPrefix"`
	IsrPrefix            string            `json:"isrPrefix"`
	IsrWriteSecret       string            `json:"isrWriteSecret,omitempty"`
	CreatedAt            int64             `json:"createdAt"`
	EdgeWorkers          *Code             `json:"edgeWorkers,omitempty"`
	ReleaseFingerprint   string            `json:"releaseFingerprint,omitempty"`
	Variables            []VariableRecord  `json:"variables,omitempty"`
	Env                  map[string]string `json:"env,omitempty"`
	Envelope             string            `json:"envelope,omitempty"`
	Needs                []edge.Need       `json:"needs,omitempty"`
	SupportInEffect      []edge.Need       `json:"supportInEffect,omitempty"`
	Waived               []edge.Need       `json:"waived,omitempty"`
}

type VariableRecord struct {
	Key     string `json:"key"`
	Folder  string `json:"folder,omitempty"`
	Version int64  `json:"version,omitempty"`
	Live    bool   `json:"live,omitempty"`
}

type Code struct {
	BundleKey   string   `json:"bundleKey"`
	ID          string   `json:"id"`
	CompatDate  string   `json:"compatDate"`
	CompatFlags []string `json:"compatFlags"`
}
