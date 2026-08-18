package edge

import (
	"context"
	"time"
)

type Kind string

const (
	KindCloudflare Kind = "cloudflare"
	KindNative     Kind = "native"
	KindNone       Kind = "none"
)

type Need string

const (
	NeedEdgeMiddleware Need = "edge-middleware"
	NeedEdgeRuntime    Need = "edge-runtime"
	NeedPPRResume      Need = "ppr-resume"
	NeedEdgeCache      Need = "edge-cache"
	NeedStreaming      Need = "streaming"
)

func AllNeeds() []Need {
	return []Need{NeedEdgeMiddleware, NeedEdgeRuntime, NeedPPRResume, NeedEdgeCache, NeedStreaming}
}

func CodeNeeds() []Need {
	return []Need{NeedEdgeMiddleware, NeedEdgeRuntime}
}

func NeedNames(needs []Need) []string {
	names := make([]string, 0, len(needs))
	for _, need := range needs {
		names = append(names, string(need))
	}
	return names
}

func ValidNeed(need Need) bool {
	for _, n := range AllNeeds() {
		if n == need {
			return true
		}
	}
	return false
}

type FlipBound struct {
	Typical   time.Duration `json:"typical"`
	Published bool          `json:"published"`
}

type Edge interface {
	Kind() Kind

	Supports(need Need) bool

	Supported() []Need

	FlipBound() FlipBound

	Bootstrap(ctx context.Context, class Class) (BootstrapOutput, error)

	Teardown(ctx context.Context, class Class) error

	Reconcile(ctx context.Context, spec StackSpec, prior StackState) (EdgeStack, error)

	Open(state StackState) (EdgeStack, error)

	ReconcilePreviewWildcard(ctx context.Context, spec PreviewWildcardSpec) (string, error)

	DestroyPreviewWildcard(ctx context.Context, baseDomain string) error

	DomainOwner(ctx context.Context, hostname string) (string, error)
}

type EdgeStack interface {
	State() StackState

	Ledger() Ledger

	Promote(ctx context.Context, promotion Promotion, pointer string) error

	RemovePointer(ctx context.Context, pointer string) (PruneResult, error)

	BindDomain(ctx context.Context, binding DomainBinding) error

	UnbindDomain(ctx context.Context, hostname string) error

	Destroy(ctx context.Context) error
}

type DomainBinding struct {
	Hostname    string
	Certificate string
}

type Ledger interface {
	SchemaVersion(ctx context.Context) (int, error)

	PutStaged(ctx context.Context, record DeploymentRecord) error

	History(ctx context.Context, pointer string) ([]HistoryEntry, error)

	Prune(ctx context.Context, keepN int, pointer string) (PruneResult, error)
}

type Programmable interface {
	AssembleApp(src WorkerSource, r Resolver) (Worker, error)

	DeployApp(ctx context.Context, app AppDeployment) (AppResult, error)

	FindApp(ctx context.Context, name string) (bool, error)

	CodeRuntime() (compatDate string, compatFlags []string)
}

type CredentialVerifier interface {
	VerifyCredentials(ctx context.Context) (CredentialIdentity, error)
}

type CredentialIdentity struct {
	Account         string
	Plan            string
	CodeEntitlement Entitlement
}

type Entitlement string

const (
	EntitlementUnknown  Entitlement = "unknown"
	EntitlementGranted  Entitlement = "granted"
	EntitlementWithheld Entitlement = "withheld"
)

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
