package edge

type Framework string

const FrameworkNext Framework = "next"

type WorkerSource struct {
	ArtifactRoot string
	BundlePath   string
	Routes       []string
}

type Assemble func(WorkerSource, Resolver) (Worker, error)
