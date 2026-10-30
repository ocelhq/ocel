package queues

import (
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	ServerlessWorkerCeiling = 15 * time.Minute
	ContainerWorkerCeiling  = 12 * time.Hour
)

var WorkerCeilings = []provider.WorkerCeiling{
	{Compute: provider.ComputeServerless, MaxDuration: ServerlessWorkerCeiling},
	{Compute: provider.ComputeContainer, MaxDuration: ContainerWorkerCeiling},
}
