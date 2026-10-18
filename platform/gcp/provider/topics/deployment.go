package topics

import (
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const cancelPoll = 500 * time.Millisecond

type Deployment struct {
	Clients  *ports.Clients
	Names    Names
	Declared map[string]*provider.TopicSpec
	Delays   Delays
}

func (d Deployment) Store() Store { return Store{Clients: d.Clients, Scope: d.Names.Scope} }

func (d Deployment) Queue() Queue {
	return Queue{Clients: d.Clients, Names: d.Names, Topics: d.Declared, Delays: d.Delays}
}

func (d Deployment) Engine(workers map[string]runs.Worker) *runs.Engine {
	return runs.New(runs.Config{
		Store:      d.Store(),
		Queue:      d.Queue(),
		Deployment: runs.Deployment{Topics: d.Declared, Workers: workers},
		CancelPoll: cancelPoll,
	})
}
