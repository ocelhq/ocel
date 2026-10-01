package topics

import (
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

type Deployment struct {
	Clients  *ports.Clients
	Names    Names
	Declared map[string]*contractv1.ManifestTopic
}

func (d Deployment) Store() Store { return Store{Clients: d.Clients, Scope: d.Names.Scope} }

func (d Deployment) Topics() Topics { return Topics{deployment: d} }

func (d Deployment) Tasks() Tasks { return Tasks{deployment: d} }
