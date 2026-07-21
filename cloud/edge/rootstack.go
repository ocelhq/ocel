package edge

import "context"

// RootStack is an optional Provider capability (ADR 0001/0002): reconciling a
// project's frozen root stack and operating the deployments store it carries.
// An edge that offers no store simply does not implement it, and the host
// runs without rollback for that edge's apps.
type RootStack interface {
	// ReconcileRootStack brings the frozen root stack up to spec.Version: on a
	// fresh project it deploys spec.Generic and spec.Store, attaches
	// spec.Domain, and mints the write secret every store operation below
	// authenticates with; on a later call it re-puts both workers only when
	// the version already deployed is behind spec.Version — an up-to-date
	// root stack is a no-op. prior is the RootStackState the last reconcile for
	// this project returned, or nil the very first time; the caller persists
	// whatever this returns, opaque, and hands it back unread here and to
	// every store operation below.
	ReconcileRootStack(ctx context.Context, spec RootStackSpec, prior RootStackState) (RootStackState, error)

	// PutStaged stages one Deployment record in the project's deployments
	// store. Staging alone can never change what is currently serving — only
	// Promote does.
	PutStaged(ctx context.Context, state RootStackState, record DeploymentRecord) error

	// Promote atomically flips the project's active-deployment pointer to
	// promotion, making every app's just-staged Deployment live together.
	Promote(ctx context.Context, state RootStackState, promotion Promotion) error

	// History returns the project's promotion history, newest first, each
	// entry marked with whether it is the currently active one.
	History(ctx context.Context, state RootStackState) ([]HistoryEntry, error)

	// DeletePromotionArtifacts deletes the Deployment records of every
	// promotion outside a keepN-deep window, always pinning the active
	// promotion so pruning can never take the live site down. It reports what
	// it removed so the caller can reclaim the app-deploy stacks and R2
	// assets those records named.
	DeletePromotionArtifacts(ctx context.Context, state RootStackState, keepN int) (PruneResult, error)
}

// RootStackSpec is what the host asks a RootStack to reconcile: the two worker
// bundles the frozen root stack carries, the deterministic names to deploy
// them under (mirroring AppDeployment.Name), the custom domain the generic
// worker serves on, and the ocel root-stack revision this deploy expects.
type RootStackSpec struct {
	// Version is the ocel root-stack revision this deploy expects. Reconcile
	// is a no-op once the deployed root stack already carries it.
	Version string
	// GenericName is the deterministic deployment identity of the frozen
	// generic app worker (ADR 0002): serves whichever Deployment the store's
	// active pointer currently names.
	GenericName string
	// Generic is the frozen generic app worker bundle.
	Generic Worker
	// StoreName is the deterministic deployment identity of the deployments
	// store worker (ticket ocelhq-u8h.1).
	StoreName string
	// Store is the deployments-store worker bundle.
	Store Worker
	// Domain is the custom hostname Generic is attached to. Empty serves it
	// on the edge's own vendor subdomain instead.
	Domain string
	// Values are what this edge reported at bootstrap, persisted verbatim by
	// the host and handed back unread — the same contract AppDeployment.Values
	// carries, so Generic's object-store binding can be sourced from it exactly
	// like a regular app worker's.
	Values map[string]string
}

// RootStackState is what ReconcileRootStack reports back: opaque to the caller,
// persisted verbatim, and handed back unread to every later RootStack call —
// the same contract BootstrapOutput.Values already carries for an edge's
// bootstrap outputs.
type RootStackState map[string]string

// Keys of a RootStackState.
const (
	// RootStackKeyEndpoint is the deployments store's HTTP endpoint, the
	// address every store operation above calls.
	RootStackKeyEndpoint = "endpoint"
	// RootStackKeyWriteSecret is the project write-secret minted at root-stack
	// creation, the credential every store operation authenticates with.
	RootStackKeyWriteSecret = "writeSecret"
)

// DeploymentRecord is one app Deployment as the deployments store holds and
// serves it. Mirrors DeploymentRecord in
// workers/deployments-store/src/store.ts — the two must agree on shape since
// the host writes this straight to the store over HTTP.
type DeploymentRecord struct {
	App             string            `json:"app"`
	BuildID         string            `json:"buildId"`
	RoutingManifest any               `json:"routingManifest"`
	FunctionURLs    map[string]string `json:"functionUrls"`
	// AssetPrefix is the full R2 key root this build's static assets were
	// uploaded under (assets/<project>/<app>/<build id>, ADR 0002 — see
	// uploadStaticAssets/appAssetR2Prefix). The frozen worker joins it
	// directly with a request's pathname; it carries no other project/app
	// identity of its own.
	AssetPrefix string `json:"assetPrefix"`
	// IsrPrefix is the full R2 key root this build's ISR cache entries and tag
	// snapshot live under (<env>/<project>/<app>/<build id>, ADR 0002 — see
	// appCaches/isrConfig.Prefix). The frozen worker roots both the cache-entry
	// reads and the tag-clock snapshot read at it. Disjoint from AssetPrefix so
	// the two lifecycles never collide.
	IsrPrefix string `json:"isrPrefix"`
	CreatedAt int64  `json:"createdAt"`
	// EdgeWorkers is a reserved, unused slot for future deployment-owned edge
	// workers (Next edge routes/middleware), carried so wiring that later
	// needs no record migration.
	EdgeWorkers any `json:"edgeWorkers,omitempty"`
}

// Promotion is the project-wide unit one production deploy produces: a
// promotion id grouping the per-app build ids it makes active. Mirrors
// Promotion in workers/deployments-store/src/store.ts.
type Promotion struct {
	PromotionID string            `json:"promotionId"`
	Ts          int64             `json:"ts"`
	Builds      map[string]string `json:"builds"`
	// Tag is the optional immutable label stamped at deploy time, unique across
	// a project's live promotions. Empty when the promotion was deployed
	// without one.
	Tag string `json:"tag,omitempty"`
}

// HistoryEntry is one Promotion in the store's ordered history, annotated
// with whether it is the currently active one. Mirrors HistoryEntry in
// workers/deployments-store/src/store.ts, whose history() returns entries
// newest-first.
type HistoryEntry struct {
	Promotion
	Active bool `json:"active"`
}

// PruneResult reports what DeletePromotionArtifacts removed. Mirrors
// PruneResult in workers/deployments-store/src/store.ts.
type PruneResult struct {
	KeptPromotionIDs    []string `json:"keptPromotionIds"`
	RemovedPromotionIDs []string `json:"removedPromotionIds"`
	// RemovedRecordKeys are the store's own "record:<app>/<buildId>" keys for
	// every record deleted (recordKey in store.ts), so the caller knows
	// exactly which underlying artifacts (stacks, R2 assets, ISR entries) it
	// still needs to reclaim.
	RemovedRecordKeys []string `json:"removedRecordKeys"`
}
