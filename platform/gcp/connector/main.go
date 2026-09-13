package main

import (
	"flag"
	"fmt"
	"os"

	"github.com/ocelhq/ocel/pkg/connectorkit"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/target"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

var version = "dev"

const (
	defaultPort = "8080"
	vendor      = "gcp"
)

func main() {
	listen := flag.String("listen", ":"+served(), "address to serve on, as host:port")
	config := flag.String("config", os.Getenv("OCEL_CONNECTOR_CONFIG"), "path to the connector config naming the console, this connector and its grants")
	reporting := flag.Bool("version", false, "print the version of this connector and exit")
	flag.Parse()

	if err := run(*listen, *config, *reporting); err != nil {
		fmt.Fprintln(os.Stderr, "ocel connector:", err)
		os.Exit(1)
	}
}

func served() string {
	if port := os.Getenv(providerkit.InjectedPortName); port != "" {
		return port
	}
	return defaultPort
}

func run(listen, config string, reporting bool) error {
	if reporting {
		fmt.Println(version)
		return nil
	}

	trust, err := connectorkit.Configured(config)
	if err != nil {
		return err
	}
	ns, err := providerkit.NamespaceFromEnv()
	if err != nil {
		return err
	}
	project, region := os.Getenv(ports.ProjectEnvVar), os.Getenv(ports.RegionEnvVar)
	if project == "" || region == "" {
		return fmt.Errorf("%s and %s name the project and region this connector reads records in, and one of them is unset",
			ports.ProjectEnvVar, ports.RegionEnvVar)
	}
	fingerprint, err := target.ForGCP(project, region, string(ns))
	if err != nil {
		return err
	}

	bindings := &ports.Clients{Namespace: ns, Project: project, Region: region}
	return connectorkit.Serve(connectorkit.Spec{
		Version:        version,
		Vendor:         vendor,
		Target:         fingerprint,
		Addr:           listen,
		Console:        trust.Console,
		ConnectorID:    trust.ConnectorID,
		OrganizationID: trust.OrganizationID,
		Grants:         trust.Grants,
		KeyPath:        trust.KeyPath,
		ConfigPath:     config,
		Vars: providerkit.Vars{
			Records: ports.Records{Clients: bindings},
			Sealer:  ports.Sealer{Clients: bindings},
		},
	})
}
