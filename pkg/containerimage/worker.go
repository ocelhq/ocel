package containerimage

import "github.com/ocelhq/ocel/pkg/buildoutput"

const (
	WorkerDir       = "/ocel/worker"
	NodeWorkerEntry = "worker.mjs"
)

func WorkerCommand(framework string) ([]string, bool) {
	switch framework {
	case buildoutput.FrameworkNode, buildoutput.FrameworkNext:
		return []string{"node", WorkerDir + "/" + NodeWorkerEntry}, true
	case buildoutput.FrameworkGo:
		return []string{WorkerDir + "/" + buildoutput.GoWorkerBinary}, true
	case buildoutput.FrameworkRust:
		return nil, true
	}
	return nil, false
}
