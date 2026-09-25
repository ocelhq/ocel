package fake

import (
	"context"
	"errors"

	"connectrpc.com/connect"
	"github.com/ocelhq/ocel/pkg/providerkit"
)

type connector struct{}

var errNoConnector = connect.NewError(connect.CodeUnimplemented,
	errors.New("this provider puts no connector on its targets; the console reaches a target of this kind through the provider itself"))

func (connector) Target(context.Context) (providerkit.ConnectorTarget, error) {
	return providerkit.ConnectorTarget{}, errNoConnector
}

func (connector) Install(context.Context, providerkit.ConnectorInstall, providerkit.Progress) (providerkit.ConnectorAddress, error) {
	return providerkit.ConnectorAddress{}, errNoConnector
}

func (connector) Remove(context.Context, providerkit.Progress) error { return errNoConnector }
