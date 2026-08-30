package edge

import (
	"context"
	"slices"
	"time"
)

type Kind string

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

func Supports(e Edge, need Need) bool {
	return slices.Contains(e.Supported(), need)
}

func ValidNeed(need Need) bool {
	return slices.Contains(AllNeeds(), need)
}

type FlipBound struct {
	Typical   time.Duration `json:"typical"`
	Published bool          `json:"published"`
}

type Facts struct {
	RunsCode              bool
	ServesUnbound         bool
	SignsOriginForwards   bool
	InvalidatesByCacheTag bool
	CredentialScope       string
}

type Edge interface {
	Kind() Kind

	Facts() Facts

	Supported() []Need

	FlipBound() FlipBound

	Bootstrap(ctx context.Context, class Class) (BootstrapOutput, error)

	Teardown(ctx context.Context, class Class) error

	Reconcile(ctx context.Context, spec StackSpec, prior StackState) (EdgeStack, error)

	Open(state StackState) (EdgeStack, error)

	ReconcilePreviewWildcard(ctx context.Context, spec PreviewWildcardSpec) (string, error)

	DestroyPreviewWildcard(ctx context.Context, baseDomain string) error

	DomainOwner(ctx context.Context, hostname string) (string, error)

	ProjectRemovals(scope ProjectScope) []PlanGroup

	PreviewWildcardRemovals(wildcard string) (removed, kept PlanGroup)

	SharedPreviewRemoval() PlanGroup
}

type ProjectScope struct {
	Slug      string
	Class     Class
	Hostnames []string
	Front     string
}

const DefaultPointer = "@production"

type EdgeStack interface {
	State() StackState

	Ledger() Ledger

	Promote(ctx context.Context, promotion Promotion, pointer string, report Reporter) error

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

type CredentialTier string

const (
	TierBootstrap CredentialTier = "bootstrap"
	TierDeploy    CredentialTier = "deploy"
)

type CredentialDocument struct {
	Heading  string
	Document string
}

type CredentialDocumenter interface {
	CredentialPermissions(tier CredentialTier) (CredentialDocument, error)
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
