package edge

import (
	"context"
	"strings"
)

// NameUnderStem reports whether a worker script name belongs to the family a
// worker-name stem heads: the stem itself, or a name segmented below it
// ("<stem>-…"). It is the one spelling of that question — a caller computing
// which workers are a project's own and an edge applying a caller-given stem
// answer it identically — and it is deliberately not a raw string prefix, so a
// name that merely starts with the same characters ("<stem>er") is a different
// family. The stem's meaning is the caller's alone: nothing here knows what a
// segment stands for. Pure.
func NameUnderStem(stem, name string) bool {
	if stem == "" || name == "" {
		return false
	}
	return name == stem || strings.HasPrefix(name, stem+"-")
}

// RootStack is an optional Provider capability (ADR 0001/0002): reconciling a
// project's frozen root stack and operating the deployments store it carries.
// An edge that offers no store simply does not implement it, and the host
// runs without rollback for that edge's apps.
type RootStack interface {
	// ReconcileRootStack brings the frozen root stack up to spec.Version: it
	// deploys spec.Generic (service-bound to the shared deployments-store
	// worker named spec.StoreScriptName and carrying spec.Slug) and attaches
	// each hostname in spec.Domains as its own worker route. It also seeds the
	// project's store instance via spec's endpoint/bootstrap credential and
	// adopts the identity that call reports back, so concurrent first deploys of
	// one slug converge on a single identity; a later call re-puts the generic
	// worker only when the version already deployed is behind spec.Version. The
	// version stamp gates the script upload alone: an up-to-date root stack
	// still reconciles spec.Domains and returns prior unchanged, so a route or
	// record that drifted — deleted out of band, or never finished being
	// attached — heals on the next deploy rather than surviving until the
	// version happens to move. The shared store worker itself is not
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
	// exactly like DeletePromotionArtifacts — it reports the same PruneResult,
	// with nothing kept. A pointer owns no edge state of its
	// own — the project's wildcard route is attached once for its lifetime — so
	// removing one is pure store work, and it never reaches anything the project
	// shares: the entrypoint worker and the store instance outlive every pointer
	// and are only ever reclaimed by an explicit project teardown. An empty
	// pointer is refused by the store so production can never be torn down
	// implicitly.
	RemovePointer(ctx context.Context, state RootStackState, pointer string) (PruneResult, error)

	// RouteOwner reports the worker script currently bound to pattern on the
	// edge — for an Ocel-deployed hostname, the project that already holds it —
	// or "" when nothing holds it. It is read-only: it creates, moves and
	// deletes nothing. It backs the deploy-time domain-claim check, which
	// refuses a hostname another project owns before anything is built.
	//
	// Matching is exact-pattern only. "*.app.com/*" and "*.preview.app.com/*"
	// are two unrelated patterns here even though traffic for the second falls
	// under the first, so an overlapping wildcard is not reported and stays the
	// late collision it is today. Closing that gap belongs to the planned
	// `ocel domains`, not here.
	RouteOwner(ctx context.Context, pattern string) (string, error)

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

	// ListDeployedWorkers returns the script names of every worker this edge has
	// deployed that falls under stem (NameUnderStem) — the stem itself and every
	// name segmented below it. It exists so the caller can hand DestroyRootStack
	// an exact set even for workers it no longer names: an earlier shape of a
	// project's deploy can have left workers standing that nothing computes a
	// name for any more, and nothing else on this interface can find them.
	// Read-only and best-effort; an edge with nothing under stem returns an
	// empty slice, not an error.
	ListDeployedWorkers(ctx context.Context, stem string) ([]string, error)

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
	// ISRWriterScriptName is the shared ISR writer worker's script name, which
	// Generic service-binds to so an edge revalidateTag raises into the build's
	// one publisher rather than writing the replica itself. Empty on a
	// substrate whose bootstrap predates the writer, which binds nothing: a
	// binding naming a script that does not exist is refused outright.
	ISRWriterScriptName string
	// StoreEndpoint is the shared deployments-store worker's HTTP endpoint,
	// where Reconcile calls /<slug>/initialize to seed the project's instance.
	StoreEndpoint string
	// BootstrapCred is the account-level bootstrap credential Reconcile
	// authenticates the one-time /<slug>/initialize call with. It authorizes
	// nothing else.
	BootstrapCred string
	// Domains are the custom hostnames Generic is attached to, each as a worker
	// route. Empty serves it on the edge's own vendor subdomain instead.
	// Production may carry several exact hostnames; a preview carries the one
	// wildcard its project declared, which every pointer is served under.
	Domains []string
	// PruneRoutes deletes any route on Generic whose hostname is not in Domains.
	// Both classes set it: Domains is the project's complete desired route set,
	// per-app for production and the project's wildcard for preview, so anything
	// else on the script is drift.
	PruneRoutes bool
	// PruneWorkerStem widens that sweep past Generic itself: with PruneRoutes on,
	// a route bound to any worker whose name falls under this stem (NameUnderStem)
	// and whose hostname is not in Domains is drift too. It is how a reconcile
	// reclaims hostnames left on workers an earlier shape of this same deploy
	// deployed and no longer uploads — a route on a script nothing puts any more
	// outlives it, and a more specific pattern wins at the edge, so it would
	// shadow Generic on that hostname forever. The stem is the caller's to
	// compute and its meaning is the caller's alone; an edge matches names
	// against it and reads nothing into the segments. Empty sweeps Generic only.
	PruneWorkerStem string
	// RequiredRecord is a DNS record Domains resolve through, which reconcile
	// verifies is present and proxied and fails otherwise, planting and
	// reclaiming nothing — for a record shared with something outside this
	// project's authority. Empty instead ensures Ocel's own proxied placeholder
	// record per hostname in Domains, which is what both classes do today: a
	// preview base domain belongs to exactly one project.
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
	// RootStackKeySecret is the per-project secret the project's instance holds,
	// which every store operation authenticates with. It is whatever the
	// instance reported at initialize — the pair this reconcile presented when
	// it seeded a fresh instance, or the pair an already-seeded one already
	// carried — never what this reconcile minted locally.
	RootStackKeySecret = "secret"
	// RootStackKeyOwnerToken is the owner token the project's instance is seeded
	// under, adopted from the same initialize response as the secret. It marks
	// the instance as this project's across a later recovery.
	RootStackKeyOwnerToken = "ownerToken"
)

// DeploymentRecord is one app Deployment as the deployments store holds and
// serves it. Mirrors DeploymentRecord in
// workers/deployments-store/src/store.ts — the two must agree on shape since
// the host writes this straight to the store over HTTP.
type DeploymentRecord struct {
	App string `json:"app"`
	// Framework is what the app declared in its manifest ("next"). One
	// entrypoint worker fronts a whole project, whose apps need not share a
	// framework, so it is the Deployment that says what can serve it: the
	// worker dispatches on this field and answers 501 for anything it has no
	// handler for. Never omitempty — an absent field reads as unsupported, so
	// a record that shipped without one could never serve.
	Framework string `json:"framework"`
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
	// IsrWriteSecret is this build's own write secret for the shared ISR writer
	// worker, whose hash the deploy seeded there (see seedISRWriters). The edge
	// worker raises this build's tag invalidations with it, and it authorizes
	// that build's slice and no other. It rides the record rather than the
	// worker script because the script is frozen and outlives every build it
	// serves. Empty on a substrate that adopted no writer.
	IsrWriteSecret string `json:"isrWriteSecret,omitempty"`
	CreatedAt      int64  `json:"createdAt"`
	// EdgeWorkers is this build's deployment-owned edge code (Next edge
	// routes/middleware): where its bundle lives and what runtime to evaluate
	// it under. Omitted when the build produced no edge output at all.
	EdgeWorkers *Code `json:"edgeWorkers,omitempty"`
	// ValueFingerprint is the digest of every variable in Variables, so two
	// Deployments that shipped different values never read alike. It is wider
	// than the fingerprint inside Identity, which covers baked values alone:
	// an app whose variables are all live rotates without a redeploy and would
	// have nothing to distinguish its Deployments by here. Empty when there is
	// nothing recorded.
	ValueFingerprint string `json:"valueFingerprint,omitempty"`
	// Variables is what this Deployment shipped with, one entry per key the app
	// resolved. It is an audit record and nothing serving reads it: the values
	// themselves ride the immutable artifact, so a rollback restores them
	// without replaying anything here.
	Variables []VariableRecord `json:"variables,omitempty"`
}

// VariableRecord names one variable a Deployment shipped with, at the store
// coordinate it resolved from. Version is the store version the value was
// taken at; Live marks a value that is fetched at runtime instead, which the
// ledger records as latest-at-runtime and never as a version, because a
// runtime fetch reads whatever the store holds then.
type VariableRecord struct {
	Key     string `json:"key"`
	Folder  string `json:"folder,omitempty"`
	Version int64  `json:"version,omitempty"`
	Live    bool   `json:"live,omitempty"`
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
// promotion id grouping the per-app Deployment identities it makes active
// (Builds carries each app's rendered identity, not its bare build id — the
// field name predates identities, like DeploymentRecord.Identity's). Mirrors
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

// PruneResult reports what DeletePromotionArtifacts or RemovePointer removed —
// the store answers both with the same shape, a removal simply keeping nothing.
// Mirrors PruneResult in workers/deployments-store/src/store.ts.
type PruneResult struct {
	KeptPromotionIDs    []string `json:"keptPromotionIds"`
	RemovedPromotionIDs []string `json:"removedPromotionIds"`
	// RemovedRecordKeys are the store's own "record:<app>/<buildId>" keys for
	// every record deleted (recordKey in store.ts), so the caller knows
	// exactly which underlying artifacts (stacks, R2 assets, ISR entries) it
	// still needs to reclaim.
	RemovedRecordKeys []string `json:"removedRecordKeys"`
	// SurvivingRecordKeys are the record keys the store still holds afterwards,
	// across every pointer. Two Deployments of one build (a rotation) share the
	// assets and edge bundle keyed by that build id, and those prefixes carry no
	// environment, so the caller reclaims them only when none of these still
	// names the build.
	SurvivingRecordKeys []string `json:"survivingRecordKeys"`
	// SurvivingPointerRecordKeys are the record keys the pruned pointer itself
	// still promotes. The ISR/prerender prefix carries the environment segment,
	// so it belongs to one pointer alone and survives only a Deployment of that
	// pointer.
	SurvivingPointerRecordKeys []string `json:"survivingPointerRecordKeys"`
}
