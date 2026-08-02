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
	// each hostname in spec.Domains as its own worker route. On a fresh project
	// it also mints an owner token and the
	// per-project secret and seeds them into the project's store instance via
	// spec's endpoint/bootstrap credential; a later call re-puts the generic
	// worker only when the version already deployed is behind spec.Version. The
	// version is per project while a route is per hostname, so an up-to-date
	// root stack still attaches spec.Domains and returns prior unchanged —
	// otherwise a second preview pointer deploying against a current root stack
	// would never get a route. The shared store worker itself is not
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

	// RemovePointer tears one pointer down outright: every promotion scoped to
	// it, the records those promotions name, and the pointer itself — pinning
	// nothing, so its active promotion goes too. It backs `ocel preview rm`,
	// which removes a whole preview. It reports the removed record keys so the
	// caller can reclaim the app-deploy stacks and R2 assets those records named,
	// exactly like DeletePromotionArtifacts; alongside them, the worker routes the
	// removed pointer owned, so the caller can delete exactly those without
	// recomputing a hostname it has no config for, and how many pointers remain,
	// so it can tell whether it just retired the project's last preview (whose
	// generic worker every preview pointer shared). An empty pointer is refused by
	// the store so production can never be torn down implicitly.
	RemovePointer(ctx context.Context, state RootStackState, pointer string) (PointerRemoval, error)

	// RemoveRoute deletes the worker route for hostname off the named worker
	// script, leaving the script and every other route attached to it serving,
	// and touching no DNS record. It backs per-pointer preview teardown: a
	// project's preview pointers all share one generic worker and hold one exact
	// route each (the hostnames RemovePointer reports), so retiring one pointer
	// must drop only that pointer's route. A route that is already gone is not an
	// error, so a re-run resumes, exactly as with DestroyRootStack.
	RemoveRoute(ctx context.Context, worker, hostname string) error

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

	// ListDeployedWorkers returns the script names of every worker the edge has
	// deployed whose name begins with prefix — a project-scoped worker-name stem
	// the caller computes. It lets a whole-project teardown find a project's
	// workers when its own record of them is gone: a shared generic worker fronts
	// every preview pointer and outlives them, so once a pointer's promotion
	// history (which names the worker) has been removed by `ocel preview rm`,
	// enumerating from the store can no longer name it. Listing from the edge
	// closes that gap. Best-effort and read-only; an edge with nothing under the
	// prefix returns an empty slice, not an error.
	ListDeployedWorkers(ctx context.Context, prefix string) ([]string, error)

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
// them under (mirroring AppDeployment.Name), the custom hostnames the generic
// worker serves on, and the ocel root-stack revision this deploy expects.
//
// Its hostname fields (PruneRoutes, RequiredRecord) state what this reconcile
// wants done, not the environment class it happens to follow from: an edge must
// never encode Ocel's environment taxonomy, so a future class is a new
// combination of these values and no provider change.
type RootStackSpec struct {
	// Version is the ocel root-stack revision this deploy expects. A root stack
	// already carrying it skips the worker upload, subdomain and version stamp;
	// the hostname work below runs either way.
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
	// Domains are the custom hostnames Generic is attached to, each as a worker
	// route. Empty serves it on the edge's own vendor subdomain instead.
	// Production may carry several; a preview carries its one pointer-exact
	// hostname.
	Domains []string
	// PruneRoutes deletes any route on Generic whose hostname is not in Domains.
	// A preview leaves it false: its shared generic worker carries one route per
	// live pointer while a reconcile knows only the pointer it is deploying, so
	// pruning would delete concurrently-deploying siblings' routes. Teardown
	// (RemoveRoute) removes a preview's route instead.
	PruneRoutes bool
	// RequiredRecord is a DNS record Domains resolve through — a "*.<base>"
	// wildcard over a preview base domain, say. Reconcile verifies it is present
	// and proxied and fails otherwise; it never plants or reclaims it, because
	// every project and pointer under that base shares it. Empty instead ensures
	// Ocel's own proxied placeholder record per hostname in Domains.
	RequiredRecord string
	// Values are what this edge reported at bootstrap, persisted verbatim by
	// the host and handed back unread — the same contract AppDeployment.Values
	// carries, so Generic's object-store binding can be sourced from it exactly
	// like a regular app worker's.
	Values map[string]string
	// Warn, when set, receives non-fatal deploy-time warnings surfaced while
	// attaching Domains (an uncovered TLS hostname, a blocking DNS record).
	// Nil is a no-op. Mirrors AppDeployment.Warn.
	Warn func(string)
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
	App string `json:"app"`
	// Identity is the Deployment's own identity — the framework build id plus
	// the fingerprint of the values baked into it — and is what the store keys
	// the record by. The wire name predates identities: a Deployment with
	// nothing baked renders as its bare build id, which is exactly what this
	// field used to carry.
	Identity        string            `json:"buildId"`
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
	// EdgeWorkers is this build's deployment-owned edge code (Next edge
	// routes/middleware): where its bundle lives and what runtime to evaluate
	// it under. Omitted when the build produced no edge output at all.
	EdgeWorkers *Code `json:"edgeWorkers,omitempty"`
	// RouteHostnames are the worker-route hostnames the deploy that produced
	// this record registered for it, so a teardown can delete exactly those
	// without recomputing them from config it does not have. A preview carries
	// its one pointer-exact hostname; production carries none — its routes are
	// project-lifetime and reconciled declaratively.
	RouteHostnames []string `json:"routeHostnames,omitempty"`
}

// Code is one build's dynamically-loaded edge code, as the frozen worker reads
// it back off the Deployment record: it GETs BundleKey from the object store and
// hands those bytes to its CodeLoader under ID. Mirrors EdgeWorkers in
// workers/deployments-store/src/store.ts.
type Code struct {
	// BundleKey is the full R2 key this build's edge bundle was uploaded
	// under. Build-scoped, never content-addressed: pruning a build reclaims
	// its whole prefix, so two builds sharing one key would let an old
	// deployment's prune delete the bundle a live one still loads.
	BundleKey string `json:"bundleKey"`
	// ID keys the loaded code in the edge's loader cache — same id, same code —
	// so it hashes the runtime settings below alongside the bundle bytes: they
	// are as much a part of what gets evaluated as the source is.
	ID          string   `json:"id"`
	CompatDate  string   `json:"compatDate"`
	CompatFlags []string `json:"compatFlags"`
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
	// SurvivingRecordKeys are the record keys the store still holds afterwards.
	// Two Deployments of one build (a rotation) share the assets, ISR entries
	// and edge bundle keyed by that build id, so the caller reclaims a build's
	// storage only when none of these still names it.
	SurvivingRecordKeys []string `json:"survivingRecordKeys"`
}

// RemovedRoute is one worker route a removed pointer owned: the app it fronted
// and the hostname it was attached to. Mirrors RemovedRoute in
// workers/deployments-store/src/store.ts — the two must agree on shape.
type RemovedRoute struct {
	App      string `json:"app"`
	Hostname string `json:"hostname"`
}

// PointerRemoval reports what RemovePointer removed: a PruneResult plus the
// routes the pointer owned and the pointers left behind. Distinct from
// PruneResult, which DeletePromotionArtifacts returns, because a prune keeps
// the pointer and its route alive. Mirrors PointerRemoval in
// workers/deployments-store/src/store.ts — the two must agree on shape.
type PointerRemoval struct {
	PruneResult
	// RemainingPointers are the preview pointers left in the project's store
	// instance after the removal, excluding the reserved production default.
	// Zero means the project just retired its last preview, so the generic
	// worker every preview pointer shared can go too.
	RemainingPointers int            `json:"remainingPointers"`
	RemovedRoutes     []RemovedRoute `json:"removedRoutes"`
}
