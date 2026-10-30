package main

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/providerhost"
	"github.com/ocelhq/ocel/pkg/refusal"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func onTheBox(hostname string) ssh.Local {
	return ssh.Local{Hostname: hostname, User: boxapi.DeployLogin, Path: boxapi.DeployPath}
}

type controlBox struct {
	hostname func() (string, error)
	conn     func(hostname string) ssh.Conn
}

func installedControlBox() controlBox {
	return controlBox{
		hostname: func() (string, error) { return readConnectorHostname(boxapi.RoutingTablePath) },
		conn:     func(hostname string) ssh.Conn { return onTheBox(hostname) },
	}
}

func controlHost(box controlBox) providerhost.Config {
	return providerhost.Config{
		Version: version,
		Base: func(context.Context, endpoint.Caller) (provider.Settings, error) {
			hostname, err := box.hostname()
			if err != nil {
				return provider.Settings{}, refusal.Refuse(refusal.CodeNotReady, "%s", err)
			}
			return provider.Settings{Options: provider.Options{"ssh": map[string]any{"host": hostname, "user": boxapi.DeployLogin}}}, nil
		},
		Serves: vps.ControlServedProcedures(),
		New: func(ctx context.Context, _ endpoint.Caller, _ string, settings provider.Settings) (provider.Provider, error) {
			hostname, err := box.hostname()
			if err != nil {
				return nil, refusal.Refuse(refusal.CodeNotReady, "%s", err)
			}
			return vps.NewControl(ctx, settings, box.conn(hostname))
		},
	}
}

func readConnectorHostname(path string) (string, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("read %s: %w", path, err)
	}
	table, err := boxapi.ReadRoutingTable(raw)
	if err != nil {
		return "", err
	}
	if table.Connector == "" {
		return "", errors.New(path + " routes no hostname to the connector: run `ocel connector add` again")
	}
	return table.Connector, nil
}
