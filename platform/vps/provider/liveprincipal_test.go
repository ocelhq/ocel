//go:build integration

package vps_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const deployLogin = "ocel-deploy"

func accountAsDecided(t *testing.T, vm liveBox) {
	t.Helper()

	entry := strings.TrimSpace(vm.ssh(t, "getent passwd "+deployLogin))
	fields := strings.Split(entry, ":")
	if len(fields) < 7 {
		t.Fatalf("getent passwd %s reads %q, and no account exists there", deployLogin, entry)
	}
	if fields[5] != "/var/lib/ocel" || fields[6] != host.LoginShell {
		t.Errorf("%s has home %q and shell %q, want /var/lib/ocel and %s", deployLogin, fields[5], fields[6], host.LoginShell)
	}
	if password := strings.TrimSpace(vm.ssh(t, "sudo getent shadow "+deployLogin+" | cut -d: -f2")); password != "*" {
		t.Errorf("%s has the password field %q, want `*`: a hash is a password and a `!` is a login sshd refuses before it reads a key", deployLogin, password)
	}
	if groups := strings.Fields(vm.ssh(t, "id -nG "+deployLogin)); len(groups) != 1 || groups[0] != deployLogin {
		t.Errorf("%s is in %v, and a login in the docker group is root by docker's own account", deployLogin, groups)
	}
	if owned := strings.TrimSpace(vm.ssh(t, "sudo stat -c '%U %a' /var/lib/ocel")); owned != "root 711" {
		t.Errorf("/var/lib/ocel is %q, want root's at 711, so %s can rename nothing a container mounts", owned, deployLogin)
	}

	rendered, err := vm.authenticates(deployLogin, "id -un")
	if err != nil {
		t.Fatalf("the deploy login refused the keys bootstrap mirrored onto it: %v", err)
	}
	if got := strings.TrimSpace(rendered); got != deployLogin {
		t.Errorf("logging in as %s lands as %q", deployLogin, got)
	}
}

func TestLiveTheDeployKeyOptionOverridesTheMirroredKeys(t *testing.T) {
	vm := liveMachine(t)
	vm.purges(t)
	vm.forgetsTheDeployLogin(t)

	named := filepath.Join(t.TempDir(), "deploy")
	keygen := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-f", named, "-N", "", "-C", "deploy-key-option")
	if out, err := keygen.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	offered, err := os.ReadFile(named + ".pub")
	if err != nil {
		t.Fatal(err)
	}

	p := vps.NewProvider(vps.TestAssets, vps.Options{
		SSH:       vps.Target{Host: vm.addr, User: vm.user, IdentityFile: vm.key, Config: vm.config},
		DeployKey: named + ".pub",
	})
	defer closing(t, p)

	ctx := context.Background()
	tier := environment.TierProduction
	bootstrap, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"}, progress.Discard()); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	defer func() {
		if err := bootstrap.Remove(ctx, tier, progress.Discard()); err != nil {
			t.Errorf("Remove() = %v", err)
		}
	}()

	written := strings.TrimSpace(vm.ssh(t, "sudo cat /var/lib/ocel/.ssh/authorized_keys"))
	if written != strings.TrimSpace(string(offered)) {
		t.Errorf("the deploy login answers to\n%s\nwant the key %q names", written, named+".pub")
	}
	if _, err := vm.authenticates(deployLogin, "true"); err == nil {
		t.Errorf("the deploy login still answers to the bootstrap login's key, and %q named another", "deployKey")
	}
}

func TestLiveBothPermissionsDocumentsDescribeTheMachineTheyBootstrap(t *testing.T) {
	vm := liveMachine(t)
	p := vm.provider(t)
	defer closing(t, p)

	ctx := context.Background()
	tier := environment.TierProduction
	bootstrap, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"}, progress.Discard()); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	defer func() {
		if err := bootstrap.Remove(ctx, tier, progress.Discard()); err != nil {
			t.Errorf("Remove() = %v", err)
		}
	}()

	bootstrapDoc, err := p.Credentials().Permissions(edge.PurposeBootstrap, environment.TierProduction)
	if err != nil {
		t.Fatalf("Permissions(bootstrap) = %v", err)
	}
	if !strings.Contains(bootstrapDoc.Document, vm.user+" ALL=") {
		t.Errorf("the bootstrap document is not about the login this run used:\n%s", bootstrapDoc.Document)
	}
	if _, err := p.Credentials().Whoami(ctx); err != nil {
		t.Errorf("Whoami() = %v, and this machine meets every requirement the bootstrap document prints", err)
	}

	deployDoc, err := p.Credentials().Permissions(edge.PurposeDeploy, environment.TierProduction)
	if err != nil {
		t.Fatalf("Permissions(deploy) = %v", err)
	}
	for _, item := range host.Items(vps.TestAssets, tier, nil, ssh.AMD64, front.Proxy{}) {
		if item.Owner != deployLogin || item.Kind == "linux:user" {
			continue
		}
		if !strings.Contains(deployDoc.Document, item.Name) {
			t.Errorf("the deploy document claims nothing about %s, which this bootstrap just handed over", item.Name)
		}
		if owner := strings.TrimSpace(vm.ssh(t, "sudo stat -c %U "+item.Name)); owner != deployLogin {
			t.Errorf("the deploy document says %s is %s's; the machine says it is %s's", item.Name, deployLogin, owner)
		}
	}
}

func TestLiveDestroyNeedsNoDeployKeyAtAll(t *testing.T) {
	vm := liveMachine(t)
	named := filepath.Join(t.TempDir(), "deploy.pub")
	keygen := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-f", strings.TrimSuffix(named, ".pub"), "-N", "", "-C", "destroy-needs-no-key")
	if out, err := keygen.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}

	ctx := context.Background()
	tier := environment.TierProduction
	installing := vps.NewProvider(vps.TestAssets, vps.Options{
		SSH:       vps.Target{Host: vm.addr, User: vm.user, IdentityFile: vm.key, Config: vm.config},
		DeployKey: named,
	})
	bootstrap, err := installing.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Tier: tier, WrittenBy: "live-suite"}, progress.Discard()); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	closing(t, installing)

	if err := os.Remove(named); err != nil {
		t.Fatal(err)
	}

	p := vps.NewProvider(vps.TestAssets, vps.Options{
		SSH:       vps.Target{Host: vm.addr, User: vm.user, IdentityFile: vm.key, Config: vm.config},
		DeployKey: named,
	})
	defer closing(t, p)
	forgetting, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	keyed := vm.provider(t)
	defer closing(t, keyed)
	reading, err := keyed.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	for _, taken := range []environment.Tier{environment.TierPreview, tier} {
		described, err := reading.Describe(ctx, taken)
		if err != nil {
			t.Fatalf("Describe(%s) = %v", taken, err)
		}
		if !described.Present {
			continue
		}
		if _, err := forgetting.PlanRemove(ctx, taken); err != nil {
			t.Fatalf("PlanRemove(%s) with the deploy key gone = %v, want a destroy that needs no key to say what it will take", taken, err)
		}
		if err := forgetting.Remove(ctx, taken, progress.Discard()); err != nil {
			t.Fatalf("Remove(%s) with the deploy key gone = %v, want a host nobody can bootstrap to still be one ocel can leave", taken, err)
		}
	}
	if left := strings.TrimSpace(vm.ssh(t, "getent passwd "+deployLogin+" || true")); left != "" {
		t.Errorf("%s still exists as %q after a keyless Remove() of every tier", deployLogin, left)
	}
}
