package topics

import (
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type Deployment struct {
	Clients  *ports.Clients
	Names    Names
	Declared map[string]*provider.TopicSpec
	Delays   Delays
}

func (d Deployment) Store() Store { return Store{Clients: d.Clients, Scope: d.Names.Scope} }

func (d Deployment) Topics() Topics { return Topics{deployment: d} }

func (d Deployment) Tasks() Tasks { return Tasks{deployment: d} }
