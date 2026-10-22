package cloudflare

type workerScript struct {
	Main          workerModule
	Modules       []workerModule
	Variables     map[string]string
	Secrets       map[string]string
	AssetBinding  string
	LoaderBinding string
	Assets        []workerAsset
	Services      map[string]string

	CachedEntrypoints []string

	DurableObjects []string
}

type workerModule struct {
	Name        string
	ContentType string
	Content     []byte
}

type workerAsset struct {
	Path    string
	Content []byte
}
