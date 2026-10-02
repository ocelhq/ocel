package containerimage_test

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/containerimage"
)

func TestAWorkerRunsFromItsAppsImageByTheEntryItsFrameworkCarries(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		framework string
		want      []string
	}{
		{framework: "node", want: []string{"node", "/ocel/worker/worker.mjs"}},
		{framework: "next", want: []string{"node", "/ocel/worker/worker.mjs"}},
		{framework: "go", want: []string{"/ocel/worker/ocel-worker"}},
	} {
		command, runs := containerimage.WorkerCommand(tc.framework)
		if !runs || !slices.Equal(command, tc.want) {
			t.Errorf("WorkerCommand(%q) = %q, %v, want %q", tc.framework, command, runs, tc.want)
		}
	}
}

func TestARustWorkerRunsTheImagesOwnCommand(t *testing.T) {
	t.Parallel()

	command, runs := containerimage.WorkerCommand("rust")
	if !runs || command != nil {
		t.Errorf("WorkerCommand(rust) = %q, %v, want the image's own command: the app's binary serves as a worker when OCEL_WORKER names one", command, runs)
	}
}

func TestAPythonImageRunsNoWorker(t *testing.T) {
	t.Parallel()

	if command, runs := containerimage.WorkerCommand("python"); runs {
		t.Errorf("WorkerCommand(python) = %q, want no worker: nothing writes a worker entry into a python image", command)
	}
}
