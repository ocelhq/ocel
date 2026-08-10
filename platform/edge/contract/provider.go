package edge

import "context"

type Kind string

const KindCloudflare Kind = "cloudflare"

type Provider interface {
	Kind() Kind

	Bootstrap(ctx context.Context, class Class) (BootstrapOutput, error)

	AssembleApp(src WorkerSource, r Resolver) (Worker, error)

	DeployApp(ctx context.Context, app AppDeployment) (AppResult, error)
}

type AppFinder interface {
	FindApp(ctx context.Context, name string) (bool, error)
}

type CredentialVerifier interface {
	VerifyCredentials(ctx context.Context) (CredentialIdentity, error)
}

type CodeLoader interface {
	CodeRuntime() (compatDate string, compatFlags []string)
}

type CredentialIdentity struct {
	Account string
}

type AppDeployment struct {
	Name    string
	Worker  Worker
	Domains []string
	Values  map[string]string
	Warn    func(string)
}

type Worker struct {
	Main          WorkerModule
	Modules       []WorkerModule
	Vars          map[string]string
	Secrets       map[string]string
	AssetBinding  string
	LoaderBinding string
	Assets        []StaticAsset
	ObjectStore   ObjectStore
	Services      map[string]string
}

type ObjectStore struct {
	Binding string
	Bucket  string
}

type WorkerModule struct {
	Name        string
	ContentType string
	Content     []byte
}

type StaticAsset struct {
	Path    string
	Content []byte
}

type AppResult struct {
	URL string
}
