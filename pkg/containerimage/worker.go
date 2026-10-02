package containerimage

import "github.com/ocelhq/ocel/pkg/buildoutput"

const (
	WorkerDir         = "/ocel/worker"
	NodeWorkerEntry   = "worker.mjs"
	NodeArtifactEntry = "ocel-worker.mjs"
)

func WorkerCommand(present func(path string) bool, imageCommand []string) []string {
	if binary := WorkerDir + "/" + buildoutput.GoWorkerBinary; present(binary) {
		return []string{binary}
	}
	if entry := WorkerDir + "/" + NodeWorkerEntry; present(entry) {
		return []string{"node", entry}
	}
	if binary := "./" + buildoutput.GoWorkerBinary; present(binary) {
		return []string{binary}
	}
	if present(NodeArtifactEntry) {
		return []string{"node", NodeArtifactEntry}
	}
	return imageCommand
}
