package edge

const RoutingManifestFile = "routing-manifest.json"

const StaticAssetDir = "static"

type WorkerSource struct {
	ArtifactRoot string
	BundlePath   string
	Entry        string
	Routes       []string
}
