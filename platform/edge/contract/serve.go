package edge

const ServeDescriptorFile = "serve.json"

// ServeDescriptor is the fact a build states about an app so an origin can
// deploy it and an edge can serve it without either knowing framework names.
// Its presence in an app's artifact root means the app is edge-served.
type ServeDescriptor struct {
	Framework string `json:"framework"`
	BuildID   string `json:"buildId"`
}
