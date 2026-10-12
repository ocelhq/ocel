package containerimage

import "github.com/ocelhq/ocel/pkg/buildoutput"

const (
	WorkerDir         = "/ocel/worker"
	NodeWorkerEntry   = "worker.mjs"
	NodeArtifactEntry = "ocel-worker.mjs"

	PythonWorkerEntry   = "worker.py"
	PythonArtifactEntry = "ocel-worker.py"
	PythonInterpreter   = "python3"
)

func WorkerCommand(present func(path string) bool, imageCommand []string) []string {
	if binary := WorkerDir + "/" + buildoutput.GoWorkerBinary; present(binary) {
		return []string{binary}
	}
	if entry := WorkerDir + "/" + NodeWorkerEntry; present(entry) {
		return []string{"node", entry}
	}
	if entry := WorkerDir + "/" + PythonWorkerEntry; present(entry) {
		return []string{PythonInterpreter, entry}
	}
	if binary := "./" + buildoutput.GoWorkerBinary; present(binary) {
		return []string{binary}
	}
	if present(NodeArtifactEntry) {
		return []string{"node", NodeArtifactEntry}
	}
	if present(PythonArtifactEntry) {
		return []string{PythonInterpreter, PythonArtifactEntry}
	}
	return imageCommand
}
