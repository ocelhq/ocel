// Package edge — PROTOTYPE of the contract after the ledger split (ocelhq/ocel#391).
package edge

import (
	"context"
	"time"

	cur "github.com/ocelhq/ocel/platform/edge/contract"
)

type Kind string

const (
	KindCloudflare Kind = "cloudflare"
	KindNative     Kind = "native"
	KindNone       Kind = "none"
)

// Need is a named fact an app's build emits and an edge declares support for.
type Need string

const (
	EdgeMiddleware Need = "edge-middleware"
	EdgeRuntime    Need = "edge-runtime"
	PPRResume      Need = "ppr-resume"
	EdgeCache      Need = "edge-cache"
)

// Edge is the contract. Every method is required; an edge that lacks one is not an edge.
type Edge interface {
	Kind() Kind

	Supports(Need) bool
	Supported() []Need

	// FlipBound is the rollback SLA: how long a pointer flip takes to be observed everywhere.
	FlipBound() FlipBound

	Bootstrap(ctx context.Context, class cur.Class) (cur.BootstrapOutput, error)
	Teardown(ctx context.Context, class cur.Class) error

	// Reconcile brings the per-project stack to spec and returns the handle;
	// Open re-attaches to a stack from persisted state without reconciling.
	Reconcile(ctx context.Context, spec StackSpec, prior StackState) (EdgeStack, error)
	Open(state StackState) (EdgeStack, error)

	// The account-wide preview wildcard (*.<base>) every project's previews resolve under.
	ReconcilePreviewWildcard(ctx context.Context, spec PreviewWildcardSpec) error
	DestroyPreviewWildcard(ctx context.Context, baseDomain string) error

	// DomainOwner reports which project holds a hostname at this edge ("" when free).
	DomainOwner(ctx context.Context, hostname string) (slug string, err error)
}

// EdgeStack is one project's edge stack: its front, its routes and its ledger.
type EdgeStack interface {
	State() StackState
	Ledger() Ledger

	// Promote records the promotion in the ledger and publishes the pointer at the front
	// (one DO write on Cloudflare; ledger write + KVS put on native; ledger write on none).
	Promote(ctx context.Context, promotion cur.Promotion, pointer string) error
	RemovePointer(ctx context.Context, pointer string) (cur.PruneResult, error)

	Destroy(ctx context.Context) error
}

// Ledger is the record store behind a stack. Who backs it is the edge's business:
// Cloudflare hosts it (Durable Object); native and none compose the origin's DynamoDB ledger.
type Ledger interface {
	SchemaVersion(ctx context.Context) (int, error)
	PutStaged(ctx context.Context, record cur.DeploymentRecord) error
	History(ctx context.Context, pointer string) ([]cur.HistoryEntry, error)
	Prune(ctx context.Context, keepN int, pointer string) (cur.PruneResult, error)
}

type FlipBound struct {
	Instant   bool
	Typical   time.Duration
	Published bool
}

type StackSpec struct {
	Version     string
	Class       cur.Class
	Slug        string
	Domains     []string
	Values      map[string]string
	PruneOnly   bool
	PruneRoutes bool
	Warn        func(string)

	// Program is the edge program (entry worker) — read only by a Programmable edge.
	Program *ProgramSpec
}

type ProgramSpec struct {
	GenericName         string
	Generic             cur.Worker
	StoreScriptName     string
	ISRWriterScriptName string
	StoreEndpoint       string
	BootstrapCred       string
	PruneWorkerStem     string
	RequiredRecord      string
}

type StackState map[string]string

const (
	StackKeySlug          = "slug"
	StackKeyEndpoint      = "endpoint"
	StackKeySecret        = "secret"
	StackKeyOwnerToken    = "ownerToken"
	StackKeyGlobalPreview = "globalPreviewDomain"
)

type PreviewWildcardSpec struct {
	Version    string
	BaseDomain string
	GrammarMin uint32
	GrammarMax uint32
	Values     map[string]string
	Warn       func(string)

	Program *ProgramSpec
}
