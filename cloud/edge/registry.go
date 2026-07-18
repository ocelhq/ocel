package edge

import "fmt"

// Framework identifies what produced an app's build output. It keys the worker
// registry alongside Kind, so supporting a framework on an edge is one registry
// entry rather than a branch inside either.
type Framework string

// FrameworkNext is Next.js. It matches the value the adapter writes into each
// function's config.json.
const FrameworkNext Framework = "next"

// WorkerSource is what a framework's assembly reads that does not come from the
// Resolver: where its build output landed, which bundle the launcher shipped for
// it, and which routes it serves.
type WorkerSource struct {
	// ArtifactRoot is the directory holding this app's build output.
	ArtifactRoot string
	// BundlePath is the compiled worker entrypoint for this framework and edge.
	BundlePath string
	// Routes are the framework-native route ids this app serves. Every one is
	// looked up through the Resolver, so an unresolvable route fails the deploy.
	Routes []string
}

// Assemble turns one app's build output into the Worker an edge uploads,
// pulling every deploy value it needs from the Resolver. It owns the binding
// names its own worker code reads, so that contract lives in one place.
type Assemble func(WorkerSource, Resolver) (Worker, error)

// workers is the framework registry. A framework absent from it needs no edge
// worker at all and its app is served straight from its function URL — which is
// how Express and Fastify already behave.
var workers = map[Framework]map[Kind]Assemble{
	FrameworkNext: {KindCloudflare: assembleNextCloudflare},
}

// NeedsWorker reports whether a framework's app is fronted by an edge worker at
// all. A framework that registers nothing needs no edge, so its app is served
// straight from its function URL.
func NeedsWorker(f Framework) bool {
	return len(workers[f]) > 0
}

// WorkerFor returns how to assemble the worker fronting a framework's app on an
// edge, erroring by naming both when that pairing has no worker.
func WorkerFor(f Framework, k Kind) (Assemble, error) {
	assemble, ok := workers[f][k]
	if !ok {
		return nil, fmt.Errorf("framework %q has no worker for edge %q", f, k)
	}
	return assemble, nil
}
