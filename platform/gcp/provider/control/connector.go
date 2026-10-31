package control

import (
	"context"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/gcp/provider/connector"
)

type connectorPort struct{ connector.Connector }

func (connectorPort) Install(context.Context, provider.ConnectorInstall, progress.Log) (provider.ConnectorAddress, error) {
	return provider.ConnectorAddress{}, provider.RefuseNeedsRunner("installing the connector")
}

func (connectorPort) Remove(context.Context, progress.Log) error {
	return provider.RefuseNeedsRunner("removing the connector")
}
