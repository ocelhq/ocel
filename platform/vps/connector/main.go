package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ocelhq/ocel/pkg/connectorkit"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/connector/hostports"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

var version = "dev"

func main() {
	listen := flag.String("listen", "unix://"+host.ConnectorSocket, "address to serve on, as unix://<path> or host:port")
	config := flag.String("config", os.Getenv("OCEL_CONNECTOR_CONFIG"), "path to the connector config naming the console, this connector and its grants")
	printing := flag.Bool("print-public-key", false, "create the connector's key if it holds none, print the public half and exit")
	reporting := flag.Bool("version", false, "print the version of this connector and exit")
	flag.Parse()

	if err := run(*listen, *config, *printing, *reporting); err != nil {
		fmt.Fprintln(os.Stderr, "ocel connector:", err)
		os.Exit(1)
	}
}

func run(listen, config string, printing, reporting bool) error {
	if reporting {
		fmt.Println(version)
		return nil
	}
	if printing {
		key, err := connectorkit.PublicKey(config)
		if err != nil {
			return err
		}
		fmt.Println(key)
		return nil
	}

	trust, err := connectorkit.LoadConfig(config)
	if err != nil {
		return err
	}
	if trust.Target == "" {
		return fmt.Errorf("%s names no target: a machine cannot tell which of its ssh host keys the console keyed this connector by, so the install writes the fingerprint into the config", config)
	}

	return connectorkit.Serve(connectorkit.Spec{
		Version:        version,
		Vendor:         "vps",
		Target:         trust.Target,
		Addr:           listen,
		Console:        trust.Console,
		ConnectorID:    trust.ConnectorID,
		OrganizationID: trust.OrganizationID,
		Grants:         trust.Grants,
		KeyPath:        trust.KeyPath,
		ConfigPath:     config,
		Vars: providerkit.Vars{
			Records: host.RecordsOver(hostports.Records{}),
			Sealer:  host.SealerOver(hostports.Sealer{}),
		},
	})
}
