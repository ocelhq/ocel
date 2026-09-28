package edge

import (
	"context"
	"slices"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/router"
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
	return slices.Contains(e.Facts().Supported, need)
}

func ValidNeed(need Need) bool {
	return slices.Contains(AllNeeds(), need)
}

type Compatibility struct {
	Date  string
	Flags []string
}

func (c Compatibility) IsZero() bool {
	return c.Date == "" && len(c.Flags) == 0
}

type Facts struct {
	Supported             []Need
	FlipBound             router.FlipBound
	Compatibility         Compatibility
	RunsCode              bool
	AddressesItself       bool
	ServesUnbound         bool
	SignsOriginForwards   bool
	ShieldsOrigin         bool
	InvalidatesByCacheTag bool
	RoutesPreviewsByLabel bool
	CachesRecords         bool
	CredentialScope       string
}

type Edge interface {
	Kind() Kind

	Facts() Facts

	Hooks() Hooks

	Bootstrap(ctx context.Context, tier environment.Tier) (BootstrapOutput, error)

	Teardown(ctx context.Context, tier environment.Tier) error

	Reconcile(ctx context.Context, spec StackSpec, prior StackState) (EdgeStack, error)

	Open(state StackState) (EdgeStack, error)

	ReconcilePreviewWildcard(ctx context.Context, spec PreviewWildcardSpec) (string, error)

	DestroyPreviewWildcard(ctx context.Context, baseDomain string) error

	DomainOwner(ctx context.Context, hostname string) (string, error)

	ProjectOwner(slug string, tier environment.Tier) string

	ProjectRemovals(scope ProjectScope) []PlanGroup

	PreviewWildcardRemovals(wildcard string) (removed, kept PlanGroup)

	SharedPreviewRemoval() PlanGroup
}

type ProjectScope struct {
	Slug      string
	Tier      environment.Tier
	Hostnames []string
	Front     string
}

type EdgeStack interface {
	State() StackState

	Ledger() Ledger

	Promote(ctx context.Context, promotion router.Promotion, pointer string, progress progress.Progress) error

	RemovePointer(ctx context.Context, pointer string, progress progress.Progress) (router.PruneResult, error)

	BindDomain(ctx context.Context, binding DomainBinding) error

	UnbindDomain(ctx context.Context, hostname string) error

	Destroy(ctx context.Context) error
}

type DomainBinding struct {
	Hostname    string
	Certificate string
	App         string
	Say         func(string)
}

type Ledger interface {
	SchemaVersion(ctx context.Context) (int, error)

	PutStaged(ctx context.Context, record router.DeploymentRecord) error

	History(ctx context.Context, pointer string) ([]router.HistoryEntry, error)

	Prune(ctx context.Context, keepN int, pointer string) (router.PruneResult, error)
}

type CredentialIdentity struct {
	Account string
}

type CodeEntitlement struct {
	Plan    string
	Granted Entitlement
	Reason  string
}

type Entitlement string

const (
	EntitlementUnknown  Entitlement = "unknown"
	EntitlementGranted  Entitlement = "granted"
	EntitlementWithheld Entitlement = "withheld"
)

type CredentialPurpose string

const (
	PurposeBootstrap CredentialPurpose = "bootstrap"
	PurposeDeploy    CredentialPurpose = "deploy"
)

type CredentialDocument struct {
	Heading  string
	Document string
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
