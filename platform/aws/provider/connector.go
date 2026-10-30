package aws

import (
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/control"
)

func (p *Provider) Connector() provider.Connector { return control.NewFullConnector(p.Provider) }
