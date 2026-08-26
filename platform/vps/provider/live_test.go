package vps_test

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/conformance"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
)

type machine struct {
	addr   string
	user   string
	key    string
	config string
}

func live(t *testing.T) machine {
	t.Helper()
	vm := machine{
		addr: os.Getenv("OCEL_INCUS_ADDR"),
		user: os.Getenv("OCEL_INCUS_USER"),
		key:  os.Getenv("OCEL_INCUS_KEY"),
	}
	if vm.addr == "" || vm.user == "" || vm.key == "" {
		t.Skip("no incus VM in the environment; run under `scripts/incus.sh run <name> -- go test ./...`")
	}

	dir := t.TempDir()
	known := filepath.Join(dir, "known_hosts")
	scanned, err := exec.Command("ssh-keyscan", "-T", "10", vm.addr).Output()
	if err != nil {
		t.Fatalf("ssh-keyscan %s: %v", vm.addr, err)
	}
	if len(strings.TrimSpace(string(scanned))) == 0 {
		t.Fatalf("ssh-keyscan %s offered no host key", vm.addr)
	}
	if err := os.WriteFile(known, scanned, 0o600); err != nil {
		t.Fatal(err)
	}

	vm.config = filepath.Join(dir, "config")
	written := fmt.Sprintf("Host *\n  UserKnownHostsFile %s\n  GlobalKnownHostsFile /dev/null\n", known)
	if err := os.WriteFile(vm.config, []byte(written), 0o600); err != nil {
		t.Fatal(err)
	}
	return vm
}

func (vm machine) provider(t *testing.T) *vps.Provider {
	t.Helper()
	return vps.NewProvider(vps.Options{SSH: vps.Target{
		Host:         vm.addr,
		User:         vm.user,
		IdentityFile: vm.key,
		Config:       vm.config,
	}})
}

func closing(t *testing.T, p *vps.Provider) {
	t.Helper()
	if err := p.Close(); err != nil {
		t.Errorf("Close() = %v, want the ssh master gone with the VM", err)
	}
}

func TestLiveTheMachineAnswersEveryPortTheConformanceSuiteAsks(t *testing.T) {
	p := live(t).provider(t)
	defer closing(t, p)

	bootstrapper, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite"}, nil); err != nil {
			t.Fatalf("Apply(%s) = %v, want the record tier every port beneath it writes into", class, err)
		}
		defer func() {
			if err := bootstrapper.Remove(ctx, class, nil); err != nil {
				t.Errorf("Remove(%s) = %v", class, err)
			}
		}()
	}

	conformance.RunPorts(t, p)
}

func TestLiveWhoamiAnswersFromTheMachineItself(t *testing.T) {
	vm := live(t)
	p := vm.provider(t)
	defer closing(t, p)

	identity, err := p.Credentials().Whoami(context.Background())
	if err != nil {
		t.Fatalf("Whoami() = %v, want the identity of a machine that is answering", err)
	}
	if identity.Provider != vps.Vendor {
		t.Errorf("Whoami().Provider = %q, want %q", identity.Provider, vps.Vendor)
	}
	if want := vm.user + "@" + vm.addr; identity.Principal != want {
		t.Errorf("Whoami().Principal = %q, want %q", identity.Principal, want)
	}
	if !strings.HasPrefix(identity.Account, "SHA256:") {
		t.Errorf("Whoami().Account = %q, want the verified host key's SHA256 fingerprint", identity.Account)
	}
	labels := named(identity.Details)
	if !labels["os"] || !labels["arch"] {
		t.Errorf("Whoami().Details = %+v, want the machine's own account of its os and arch", identity.Details)
	}
}

func named(details []providerkit.Detail) map[string]bool {
	labels := map[string]bool{}
	for _, detail := range details {
		labels[detail.Label] = detail.Value != ""
	}
	return labels
}
