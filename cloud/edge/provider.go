// Package edge is the contract between a cloud provider and the edge running in
// front of it. A provider bootstraps and deploys through Provider without
// knowing which edge is configured; an edge implements Provider without knowing
// which cloud it fronts or which framework produced the worker it uploads.
package edge

import "context"

// Kind identifies an edge implementation. It keys the framework registry, so a
// framework declares support for an edge by registering under its Kind rather
// than by being referenced from the edge's own code.
type Kind string

// KindCloudflare is the Cloudflare Workers edge, the default and — for now —
// only edge.
const KindCloudflare Kind = "cloudflare"

// Provider is an edge: somewhere a framework's worker runs in front of the
// cloud provider's compute. Implementing it is the whole of adding an edge.
type Provider interface {
	// Kind names which edge this is, so a provider can look up the worker a
	// framework registered for it without knowing what it configured.
	Kind() Kind

	// Bootstrap provisions whatever the edge needs to exist before any deploy of
	// one substrate class, and reports its trust posture, its outputs, and any
	// resources it offers the provider. It runs before the provider's own
	// bootstrap.
	Bootstrap(ctx context.Context, class Class) (BootstrapOutput, error)

	// DeployApp uploads one app's assembled worker and returns where it is
	// served. The edge receives exactly one app, already assembled, so it never
	// filters a project-wide manifest or understands a framework.
	DeployApp(ctx context.Context, app AppDeployment) (AppResult, error)
}

// AppFinder is an optional Provider capability: reporting whether a deployment
// already exists under a name. A provider uses it to notice deployments an
// earlier naming scheme left behind, so it can report rather than orphan them.
// An edge whose API cannot answer the question simply does not implement it.
type AppFinder interface {
	FindApp(ctx context.Context, name string) (bool, error)
}

// AppDeployment is one app's fully-resolved edge deployment: everything read off
// disk and computed by the provider, so the edge only talks to its own API.
type AppDeployment struct {
	// Name is the app's deterministic deployment identity. Redeploys of the same
	// app reuse it, so the edge updates in place rather than accumulating
	// deployments.
	Name string
	// Worker is the assembled bundle and bindings to upload under Name.
	Worker Worker
	// Domain is the custom hostname the app is served on. Empty serves it on the
	// edge's own vendor subdomain instead.
	Domain string
	// Values are what this edge reported at bootstrap, persisted verbatim by the
	// provider and handed back unread, so the edge can see what it provisioned
	// without re-querying its own API.
	Values map[string]string
}

// Worker is a framework's edge bundle: the entrypoint, the modules shipping
// alongside it, its bindings, and the static files served next to it. It is
// what a framework registry entry produces and what an edge uploads.
type Worker struct {
	// Main is the worker entrypoint (a module-syntax fetch handler).
	Main WorkerModule
	// Modules are additional modules uploaded alongside Main and resolvable by
	// its imports — a routing manifest, say.
	Modules []WorkerModule
	// Vars are plain-text bindings surfaced on the worker's env.
	Vars map[string]string
	// Secrets are bindings surfaced on the worker's env whose values must never
	// appear as plaintext in the uploaded metadata.
	Secrets map[string]string
	// AssetBinding is the env name the worker reads its static-asset fetcher
	// from. Empty when the worker serves no static assets.
	AssetBinding string
	// Assets are the truly-static files served alongside the worker.
	Assets []StaticAsset
	// ObjectStore is the store the worker reads through an edge-native binding
	// rather than signed HTTP. Empty when the worker reads no object store.
	ObjectStore ObjectStore
	// Services are the worker's bindings to other workers deployed under the
	// same edge, keyed by the env name the worker reads each binding from and
	// valued by the deployment identity (script name) it should resolve to.
	Services map[string]string
}

// ObjectStore is a worker's binding to bulk storage the edge itself serves: the
// env name the worker reads the store from, and the bucket bound there. Both
// halves are named generically — an edge maps them onto whatever object store it
// runs, exactly as it maps AssetBinding onto its own asset fetcher — so no
// vendor's product enters the contract.
//
// A framework asks for a store by Binding alone; the edge fills Bucket with what
// it provisioned. Neither half alone is a binding: an edge with no store for the
// worker to read leaves Bucket empty and uploads no binding at all.
type ObjectStore struct {
	Binding string
	Bucket  string
}

// WorkerModule is one module of a worker upload: a name (as the entrypoint's
// imports reference it), its content type, and its bytes.
type WorkerModule struct {
	Name        string
	ContentType string
	Content     []byte
}

// StaticAsset is one file served alongside the worker, keyed by its URL path
// (e.g. "/next.svg"). It carries raw bytes: an edge whose upload session keys
// files by a content hash computes that hash in the form its own API requires,
// so no framework has to know one edge's hashing rules.
type StaticAsset struct {
	Path    string
	Content []byte
}

// AppResult reports where a deployed app is served.
type AppResult struct {
	URL string
}
