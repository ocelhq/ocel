package framework

import (
	"fmt"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/edge/contract/framework/nextjs"
)

var workers = map[edge.Framework]map[edge.Kind]edge.Assemble{
	edge.FrameworkNext: {edge.KindCloudflare: nextjs.AssembleCloudflare},
}

func NeedsWorker(f edge.Framework) bool {
	return len(workers[f]) > 0
}

func WorkerFor(f edge.Framework, k edge.Kind) (edge.Assemble, error) {
	assemble, ok := workers[f][k]
	if !ok {
		return nil, fmt.Errorf("framework %q has no worker for edge %q", f, k)
	}
	return assemble, nil
}
