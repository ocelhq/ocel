package containerimage_test

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/containerimage"
)

func holding(paths ...string) func(string) bool {
	return func(path string) bool { return slices.Contains(paths, path) }
}

func TestAWorkerRunsTheEntryItsImageOrArtifactCarries(t *testing.T) {
	t.Parallel()

	served := []string{"pnpm", "start"}
	for _, tc := range []struct {
		name  string
		image func(string) bool
		want  []string
	}{
		{name: "a go entry", image: holding("/ocel/worker/ocel-worker"), want: []string{"/ocel/worker/ocel-worker"}},
		{name: "a node entry", image: holding("/ocel/worker/worker.mjs"), want: []string{"node", "/ocel/worker/worker.mjs"}},
		{name: "a go function artifact", image: holding("./ocel-worker"), want: []string{"./ocel-worker"}},
		{name: "a node function artifact", image: holding("ocel-worker.mjs"), want: []string{"node", "ocel-worker.mjs"}},
		{name: "an image entry beside an artifact's", image: holding("ocel-worker.mjs", "/ocel/worker/worker.mjs"), want: []string{"node", "/ocel/worker/worker.mjs"}},
		{name: "no entry, as a rust binary that serves as a worker itself", image: holding(), want: served},
	} {
		if got := containerimage.WorkerCommand(tc.image, served); !slices.Equal(got, tc.want) {
			t.Errorf("WorkerCommand(%s) = %q, want %q", tc.name, got, tc.want)
		}
	}
}
