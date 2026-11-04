package main

import (
	"context"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/providerhost"
	"github.com/ocelhq/ocel/pkg/variablestoreserver"
	"github.com/ocelhq/ocel/platform/vps/box/recordstore"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

var version = "dev"

func main() {
	listen := flag.String("listen", "unix://"+boxapi.ConnectorSocket, "address to serve on, as unix://<path> or host:port")
	config := flag.String("config", os.Getenv("OCEL_CONNECTOR_CONFIG"), "path to the endpoint config")
	printing := flag.Bool("print-public-key", false, "print the connector's public key, creating it if absent, and exit")
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
		key, err := endpoint.EnsurePublicKey(config)
		if err != nil {
			return err
		}
		fmt.Println(key)
		return nil
	}

	trust, err := endpoint.ReadConfig(config)
	if err != nil {
		return err
	}
	if trust.Target == "" {
		return fmt.Errorf("%s names no target ssh host key fingerprint\nReinstall the connector to write it", config)
	}

	served, err := readRoles(installedBoxFiles(), trust, os.Stderr)
	if err != nil {
		return err
	}
	if served.trust != nil {
		trust.Credentials = served.trust
	}
	if served.runner != nil {
		sweeping, stop := context.WithCancel(context.Background())
		defer stop()
		defer served.runner.Close()
		go served.runner.SweepEvery(sweeping, sweepInterval)
	}

	control, err := providerhost.New(controlHost(installedControlBox()))
	if err != nil {
		return err
	}
	defer control.Close()
	serving := append([]endpoint.Mount{variablesMount(boxapi.StateRoot)}, served.mounts()...)
	serving = append(serving, control.Mounts()...)

	return endpoint.Serve(endpoint.Spec{
		Config:     trust,
		Version:    version,
		Vendor:     "vps",
		Addr:       listen,
		ConfigPath: config,
		Mounts:     serving,
	})
}

const sweepInterval = time.Minute

func variablesMount(root string) endpoint.Mount {
	return endpoint.VariablesMount(variablestoreserver.FixedBackend{
		KeyValues: recordstore.KeyValues{Root: root},
		Cipher:    newAgentCipher(),
	})
}
