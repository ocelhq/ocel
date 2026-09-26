package fake

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

type connector struct{}

var errNoConnector = connect.NewError(connect.CodeUnimplemented,
	errors.New("this provider puts no connector on its targets; the console reaches a target of this kind through the provider itself"))

func (connector) Target(context.Context) (provider.ConnectorTarget, error) {
	return provider.ConnectorTarget{}, errNoConnector
}

func (connector) Install(context.Context, provider.ConnectorInstall, edge.Progress) (provider.ConnectorAddress, error) {
	return provider.ConnectorAddress{}, errNoConnector
}

func (connector) Remove(context.Context, edge.Progress) error { return errNoConnector }
