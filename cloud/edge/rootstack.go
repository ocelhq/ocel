package edge

import "context"

// RootStack is an optional Provider capability (ADR 0001/0002): reconciling a
// project's frozen root stack and operating the deployments store it carries.
// An edge that offers no store simply does not implement it, and the host
// runs without rollback for that edge's apps.
type RootStack interface {
	// ReconcileRootStack brings the frozen root stack up to spec.Version: it
	// deploys spec.Generic (service-bound to the shared deployments-store
	// worker named spec.StoreScriptName and carrying spec.Slug) and attaches
	// spec.Domain. On a fresh project it also mints an owner token and the
	// per-project secret and seeds them into the project's store instance via
	// spec's endpoint/bootstrap credential; a later call re-puts the generic
	// worker only when the version already deployed is behind spec.Version — an
	// up-to-date root stack is a no-op. The shared store worker itself is not
	// deployed here (it is provisioned once at bootstrap). prior is the
	// RootStackState the last reconcile for this project returned, or nil the
	// very first time; the caller persists whatever this returns, opaque, and
	// hands it back unread here and to every store operation below.
	ReconcileRootStack(ctx context.Context, spec RootStackSpec, prior RootStackState) (RootStackState, error)

	// PutStaged stages one Deployment record in the project's deployments
	// store. Staging alone can never change what is currently serving — only
	// Promote does.
	PutStaged(ctx context.Context, state RootStackState, record DeploymentRecord) error

	// Promote atomically flips a named pointer to promotion, making every app's
	// just-staged Deployment live together under that pointer. An empty pointer
	// moves the reserved production default (the primary domain resolves it); a
	// preview passes its own pointer (the subdomain slug or persistent name), so
	// production and every preview retain independent active deployments in the
	// same store instance.
	Promote(ctx context.Context, state RootStackState, promotion Promotion, pointer string) error

	// History returns a pointer's promotion history, newest first, each entry
	// marked with whether it is the currently active one for that pointer. An
	// empty pointer scopes to the production default.
	History(ctx context.Context, state RootStackState, pointer string) ([]HistoryEntry, error)

	// DeletePromotionArtifacts deletes the Deployment records of every promotion
	// outside a keepN-deep window for one pointer, always pinning that pointer's
	// active promotion so pruning can never take a live deployment down. An empty
	// pointer scopes to the production default. It reports what it removed so the
	// caller can reclaim the app-deploy stacks and R2 assets those records named.
	DeletePromotionArtifacts(ctx context.Context, state RootStackState, keepN int, pointer string) (PruneResult, error)

	// DestroyRootStack deletes every worker named in workers — the project's
	// generic worker(s) — detaching each one's custom-domain binding first
	// (detaching the domain but never deleting DNS records the user owns). The
	// shared deployments-store worker is never among them: it is provisioned
	// once at bootstrap and outlives any single project; a project's own store
	// data is reclaimed by DestroyInstance instead. workers is the exact,
	// caller-computed set to remove, so the edge deletes precisely those and
	// never has to guess a project's workers from a name prefix; a name already
	// gone is not an error, so a re-run resumes. Best-effort: it attempts every
	// worker and joins any failures. Backs the root-stack half of `ocel destroy`.
	DestroyRootStack(ctx context.Context, workers []string) error

	// DestroyInstance wipes the project's own instance in the shared
	// deployments-store worker — its promotion history, records, ownership and
	// secret — leaving the shared worker and every other project's instance
	// untouched, and freeing the slug for reuse. Authenticated with the
	// project secret in state. A slug that was never initialized is not an
	// error, so a re-run resumes. Backs the store half of `ocel destroy`.
	DestroyInstance(ctx context.Context, state RootStackState) error
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
	// Slug is the project's stable deployment identity: it keys the project's
	// own instance in the shared deployments-store worker, and is bound onto
	// the generic worker so its service-binding RPCs address that instance.
	Slug string
	// StoreScriptName is the shared deployments-store worker's script name
	// (provisioned once at bootstrap), which Generic service-binds to.
	StoreScriptName string
	// StoreEndpoint is the shared deployments-store worker's HTTP endpoint,
	// where Reconcile calls /<slug>/initialize to seed the project's instance.
	StoreEndpoint string
	// BootstrapCred is the account-level bootstrap credential Reconcile
	// authenticates the one-time /<slug>/initialize call with. It authorizes
	// nothing else.
	BootstrapCred string
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
	// RootStackKeySlug is the project's slug, addressing its own instance in
	// the shared deployments-store worker (idFromName) — the leading path
	// segment of every store operation.
	RootStackKeySlug = "slug"
	// RootStackKeyEndpoint is the shared deployments store's HTTP endpoint, the
	// address every store operation calls.
	RootStackKeyEndpoint = "endpoint"
	// RootStackKeySecret is the per-project secret, minted on the project's
	// first reconcile and seeded into its instance, that every store operation
	// authenticates with.
	RootStackKeySecret = "secret"
	// RootStackKeyOwnerToken is the self-minted owner token seeded into the
	// project's instance, presented on a later reconcile to distinguish
	// legitimate recovery from a slug collision.
	RootStackKeyOwnerToken = "ownerToken"
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
