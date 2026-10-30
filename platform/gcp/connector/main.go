package main

import (
	"context"
	"flag"
	"fmt"
	"os"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/providerhost"
	"github.com/ocelhq/ocel/pkg/target"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
	"github.com/ocelhq/ocel/platform/gcp/provider/control"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

var version = "dev"

const (
	defaultPort = "8080"
	vendor      = "gcp"
)

func main() {
	listen := flag.String("listen", ":"+readPort(), "address to serve on, as host:port")
	config := flag.String("config", os.Getenv("OCEL_CONNECTOR_CONFIG"), "path to the endpoint config naming the console, this connector and the scopes it allows")
	reporting := flag.Bool("version", false, "print the version of this connector and exit")
	flag.Parse()

	if err := run(*listen, *config, *reporting); err != nil {
		fmt.Fprintln(os.Stderr, "ocel connector:", err)
		os.Exit(1)
	}
}

func readPort() string {
	if port := os.Getenv(containerimage.PortEnvVar); port != "" {
		return port
	}
	return defaultPort
}

func run(listen, config string, reporting bool) error {
	if reporting {
		fmt.Println(version)
		return nil
	}

	trust, err := endpoint.ReadConfig(config)
	if err != nil {
		return err
	}
	ns, err := provider.NamespaceFromEnv()
	if err != nil {
		return err
	}
	project, region := os.Getenv(ports.ProjectEnvVar), os.Getenv(ports.RegionEnvVar)
	if project == "" || region == "" {
		return fmt.Errorf("%s and %s name the project and region this connector reads records in, and one of them is unset",
			ports.ProjectEnvVar, ports.RegionEnvVar)
	}
	account := os.Getenv(ports.ConnectorAccountEnvVar)
	if account == "" {
		return fmt.Errorf("%s names the service account Cloud Scheduler calls this connector as to make it beat, and it is unset",
			ports.ConnectorAccountEnvVar)
	}
	trust.Target, err = target.Fingerprint(vendor, project, region, string(ns))
	if err != nil {
		return err
	}

	bindings := &ports.Clients{Namespace: ns, Project: project, Region: region}
	bindings.Records = ports.KeyValues{Clients: bindings}
	host, err := providerhost.New(controlHost(project, region))
	if err != nil {
		return err
	}
	defer host.Close()
	return endpoint.Serve(endpoint.Spec{
		Config:          trust,
		Version:         version,
		Vendor:          vendor,
		Addr:            listen,
		ConfigPath:      config,
		Mounts:          mounts(host, bindings),
		VerifyHeartbeat: scheduledCaller{keys: endpoint.NewKeyring(googleCertsURL), account: account}.verify,
	})
}

func mounts(host *providerhost.Host, bindings *ports.Clients) []endpoint.Mount {
	return append(host.Mounts(), endpoint.VariablesMount(variableStore(bindings)))
}

func controlHost(project, region string) providerhost.Config {
	return providerhost.Config{
		Version: version,
		Base: func(context.Context, endpoint.Caller) (provider.Settings, error) {
			return provider.Settings{Options: provider.Options{"project": project, "region": region}}, nil
		},
		Serves: control.ServedProcedures(),
		New: func(ctx context.Context, _ endpoint.Caller, _ string, settings provider.Settings) (provider.Provider, error) {
			p, err := control.New(ctx, settings)
			if err != nil {
				return nil, err
			}
			return p, nil
		},
	}
}

func variableStore(bindings *ports.Clients) variablestoreserver.FixedBackend {
	return variablestoreserver.FixedBackend{
		KeyValues:     ports.KeyValues{Clients: bindings},
		Cipher:        ports.Cipher{Clients: bindings},
		ProveIdentity: ports.ProveIdentity,
	}
}
