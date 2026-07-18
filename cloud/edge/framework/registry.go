// Package framework is the registry pairing a framework with the edge it runs
// on. It is the one place that knows both sets of names: frameworks live in its
// subpackages and edges in their own modules, and neither imports the other.
// Supporting a framework on an edge is one entry here.
package framework

import (
	"fmt"

	"github.com/ocelhq/ocel/cloud/edge"
	"github.com/ocelhq/ocel/cloud/edge/framework/nextjs"
)

// workers is the framework registry. A framework absent from it needs no edge
// worker at all and its app is served straight from its function URL — which is
// how Express and Fastify already behave.
var workers = map[edge.Framework]map[edge.Kind]edge.Assemble{
	edge.FrameworkNext: {edge.KindCloudflare: nextjs.AssembleCloudflare},
}

// NeedsWorker reports whether a framework's app is fronted by an edge worker at
// all. A framework that registers nothing needs no edge, so its app is served
// straight from its function URL.
func NeedsWorker(f edge.Framework) bool {
	return len(workers[f]) > 0
}

// WorkerFor returns how to assemble the worker fronting a framework's app on an
// edge, erroring by naming both when that pairing has no worker.
func WorkerFor(f edge.Framework, k edge.Kind) (edge.Assemble, error) {
	assemble, ok := workers[f][k]
	if !ok {
		return nil, fmt.Errorf("framework %q has no worker for edge %q", f, k)
	}
	return assemble, nil
}
