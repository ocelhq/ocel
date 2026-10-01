package containerimage

import "github.com/ocelhq/ocel/pkg/buildoutput"

const (
	WorkerDir       = "/ocel/worker"
	NodeWorkerEntry = "worker.mjs"
)

func WorkerCommand(present func(path string) bool, imageCommand []string) []string {
	if binary := WorkerDir + "/" + buildoutput.GoWorkerBinary; present(binary) {
		return []string{binary}
	}
	if entry := WorkerDir + "/" + NodeWorkerEntry; present(entry) {
		return []string{"node", entry}
	}
	return imageCommand
}
