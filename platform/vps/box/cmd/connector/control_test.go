package main

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/proto/provider/contract/v1/contractv1connect"
	"github.com/ocelhq/ocel/pkg/providerhost"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const installCap = 128 << 20

func TestTheConnectorLinksNoneOfTheBinariesABootstrapInstalls(t *testing.T) {
	t.Parallel()

	out, err := exec.Command("go", "list", "-deps", ".").CombinedOutput()
	if err != nil {
		t.Fatalf("go list -deps: %v\n%s", err, out)
	}
	for _, pkg := range strings.Fields(string(out)) {
		if strings.HasSuffix(pkg, "/platform/vps/provider/host/assets") {
			t.Errorf("the vps connector links %s, and with it every box binary a bootstrap installs", pkg)
		}
	}
}

func TestTheConnectorFitsTheInstallCapWithRoomToSpare(t *testing.T) {
	t.Parallel()

	binary := filepath.Join(t.TempDir(), "connector")
	build := exec.Command("go", "build", "-trimpath", "-ldflags=-s -w", "-o", binary, ".")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64")
	if out, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the connector: %v\n%s", err, out)
	}
	info, err := os.Stat(binary)
	if err != nil {
		t.Fatal(err)
	}
	if limit := int64(installCap) * 9 / 10; info.Size() > limit {
		t.Errorf("the connector is %d bytes, over %d, nine tenths of the %d an install carries", info.Size(), limit, installCap)
	}
}

func TestTheBoxServesControlCallsFromItsOwnProvider(t *testing.T) {
	t.Parallel()

	control, err := providerhost.New(controlHost(fixedBox("box.example.com", onTheBox("box.example.com"))))
	if err != nil {
		t.Fatal(err)
	}
	defer control.Close()
	if services := (endpoint.Spec{Mounts: control.Mounts()}).Services(); !slices.Contains(services, contractv1connect.ProviderServiceName) {
		t.Errorf("services = %v, want the provider service", services)
	}

	config := controlHost(fixedBox("box.example.com", onTheBox("box.example.com")))
	settings, err := config.Base(context.Background(), endpoint.Caller{})
	if err != nil {
		t.Fatal(err)
	}
	p, err := config.New(context.Background(), endpoint.Caller{}, "", settings)
	if err != nil {
		t.Fatal(err)
	}
	var refused refusal.Refusal
	if _, err := p.Bootstrap(edge.None); !errors.As(err, &refused) || refused.Code != refusal.CodeNeedsRunner {
		t.Errorf("Bootstrap() = %v, want it sent to a runner", err)
	}
}

func TestTheConnectorsHostnameIsTheOneTheRoutingTableRoutesToIt(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	routed, err := boxapi.WriteRoutingTable(boxapi.RoutingTable{Connector: "box.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "table.json")
	written(t, path, routed)
	if hostname, err := readConnectorHostname(path); err != nil || hostname != "box.example.com" {
		t.Errorf("readConnectorHostname = %q, %v", hostname, err)
	}

	unrouted, err := boxapi.WriteRoutingTable(boxapi.RoutingTable{})
	if err != nil {
		t.Fatal(err)
	}
	written(t, path, unrouted)
	if _, err := readConnectorHostname(path); err == nil || !strings.Contains(err.Error(), "ocel connector add") {
		t.Errorf("err = %v, want the command that routes it", err)
	}
}

func fixedBox(hostname string, conn ssh.Conn) controlBox {
	return controlBox{
		hostname: func() (string, error) { return hostname, nil },
		conn:     func(string) ssh.Conn { return conn },
	}
}

func TestTheConnectorServesControlCallsOnceTheRoutingTableNamesItsHostname(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "table.json")
	box := controlBox{
		hostname: func() (string, error) { return readConnectorHostname(path) },
		conn:     func(hostname string) ssh.Conn { return onTheBox(hostname) },
	}
	config := controlHost(box)
	if _, err := config.Base(context.Background(), endpoint.Caller{}); err == nil {
		t.Fatal("Base() named a box before the routing table names its hostname")
	}

	routed, err := boxapi.WriteRoutingTable(boxapi.RoutingTable{Connector: "box.example.com"})
	if err != nil {
		t.Fatal(err)
	}
	written(t, path, routed)
	settings, err := config.Base(context.Background(), endpoint.Caller{})
	if err != nil {
		t.Fatalf("Base() after the route was written = %v, want the box named without a restart", err)
	}
	if host := settings.Options["ssh"].(map[string]any)["host"]; host != "box.example.com" {
		t.Errorf("ssh host = %v, want the routed hostname", host)
	}
}
