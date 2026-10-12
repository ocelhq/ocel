package containerimage

import "github.com/ocelhq/ocel/pkg/buildoutput"

const WorkerDir = "/ocel/worker"

type WorkerEntry struct {
	Interpreter  string
	ImageFile    string
	ArtifactFile string
}

var (
	GoWorkerEntry     = WorkerEntry{ImageFile: buildoutput.GoWorkerBinary, ArtifactFile: "./" + buildoutput.GoWorkerBinary}
	NodeWorkerEntry   = WorkerEntry{Interpreter: "node", ImageFile: "worker.mjs", ArtifactFile: "ocel-worker.mjs"}
	PythonWorkerEntry = WorkerEntry{Interpreter: "python3", ImageFile: "worker.py", ArtifactFile: "ocel-worker.py"}
)

var workerEntries = []WorkerEntry{GoWorkerEntry, NodeWorkerEntry, PythonWorkerEntry}

func (e WorkerEntry) ImageCommand() []string {
	return e.commandFor(WorkerDir + "/" + e.ImageFile)
}

func (e WorkerEntry) ArtifactCommand() []string {
	return e.commandFor(e.ArtifactFile)
}

func (e WorkerEntry) commandFor(path string) []string {
	if e.Interpreter == "" {
		return []string{path}
	}
	return []string{e.Interpreter, path}
}

func WorkerCommand(present func(path string) bool, imageCommand []string) []string {
	for _, entry := range workerEntries {
		if present(WorkerDir + "/" + entry.ImageFile) {
			return entry.ImageCommand()
		}
	}
	for _, entry := range workerEntries {
		if present(entry.ArtifactFile) {
			return entry.ArtifactCommand()
		}
	}
	return imageCommand
}
