package vps_test

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const deployLogin = "ocel-deploy"

func standsAsDecided(t *testing.T, vm machine) {
	t.Helper()

	entry := strings.TrimSpace(vm.ssh(t, "getent passwd "+deployLogin))
	fields := strings.Split(entry, ":")
	if len(fields) < 7 {
		t.Fatalf("getent passwd %s reads %q, and no account stands there", deployLogin, entry)
	}
	if fields[5] != "/var/lib/ocel" || fields[6] != "/bin/sh" {
		t.Errorf("%s has home %q and shell %q, want /var/lib/ocel and /bin/sh", deployLogin, fields[5], fields[6])
	}
	if held := strings.TrimSpace(vm.ssh(t, "sudo getent shadow "+deployLogin+" | cut -d: -f2")); held != "*" {
		t.Errorf("%s carries the password field %q, want `*`: a hash is a password and a `!` is a login sshd refuses before it reads a key", deployLogin, held)
	}
	if groups := vm.ssh(t, "id -nG "+deployLogin); !strings.Contains(groups, "docker") {
		t.Errorf("%s is in %q, and a deploy that cannot reach the docker socket deploys nothing", deployLogin, strings.TrimSpace(groups))
	}
	if mode := strings.TrimSpace(vm.ssh(t, "sudo stat -c %a /var/lib/ocel")); mode != "750" {
		t.Errorf("/var/lib/ocel stands at %q, want 750", mode)
	}

	rendered, err := vm.attempt(deployLogin, "id -un")
	if err != nil {
		t.Fatalf("the deploy login refused the keys bootstrap mirrored onto it: %v", err)
	}
	if got := strings.TrimSpace(rendered); got != deployLogin {
		t.Errorf("logging in as %s lands as %q", deployLogin, got)
	}
}

func TestLiveTheDeployKeyOptionOverridesTheMirroredKeys(t *testing.T) {
	vm := live(t)
	vm.ssh(t, "sudo rm -rf /etc/ocel /var/lib/ocel /usr/local/lib/ocel")
	vm.ssh(t, "sudo userdel "+deployLogin+" 2>/dev/null || true")

	named := filepath.Join(t.TempDir(), "deploy")
	keygen := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-f", named, "-N", "", "-C", "deploy-key-option")
	if out, err := keygen.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}
	offered, err := os.ReadFile(named + ".pub")
	if err != nil {
		t.Fatal(err)
	}

	p := vps.NewProvider(vps.Options{
		SSH:       vps.Target{Host: vm.addr, User: vm.user, IdentityFile: vm.key, Config: vm.config},
		DeployKey: named + ".pub",
	})
	defer closing(t, p)

	ctx := context.Background()
	class := providerkit.ClassProduction
	bootstrapper, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	defer func() {
		if err := bootstrapper.Remove(ctx, class, nil); err != nil {
			t.Errorf("Remove() = %v", err)
		}
	}()

	written := strings.TrimSpace(vm.ssh(t, "sudo cat /var/lib/ocel/.ssh/authorized_keys"))
	if written != strings.TrimSpace(string(offered)) {
		t.Errorf("the deploy login answers to\n%s\nwant the key %q names", written, named+".pub")
	}
	if _, err := vm.attempt(deployLogin, "true"); err == nil {
		t.Errorf("the deploy login still answers to the bootstrap login's key, and %q named another", "deployKey")
	}
}

func TestLiveBothPermissionsDocumentsDescribeTheMachineTheyBootstrap(t *testing.T) {
	vm := live(t)
	p := vm.provider(t)
	defer closing(t, p)

	ctx := context.Background()
	class := providerkit.ClassProduction
	bootstrapper, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	defer func() {
		if err := bootstrapper.Remove(ctx, class, nil); err != nil {
			t.Errorf("Remove() = %v", err)
		}
	}()

	bootstrapDoc, err := p.Credentials().Permissions(providerkit.TierBootstrap)
	if err != nil {
		t.Fatalf("Permissions(bootstrap) = %v", err)
	}
	if !strings.Contains(bootstrapDoc.Document, vm.user+" ALL=") {
		t.Errorf("the bootstrap document is not about the login this run used:\n%s", bootstrapDoc.Document)
	}
	if _, err := p.Credentials().Whoami(ctx); err != nil {
		t.Errorf("Whoami() = %v, and this machine meets every requirement the bootstrap document prints", err)
	}

	deployDoc, err := p.Credentials().Permissions(providerkit.TierDeploy)
	if err != nil {
		t.Fatalf("Permissions(deploy) = %v", err)
	}
	for _, item := range host.Items(class, nil, host.ArchAMD64) {
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
	vm := live(t)
	named := filepath.Join(t.TempDir(), "deploy.pub")
	keygen := exec.Command("ssh-keygen", "-q", "-t", "ed25519", "-f", strings.TrimSuffix(named, ".pub"), "-N", "", "-C", "destroy-needs-no-key")
	if out, err := keygen.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen: %v\n%s", err, out)
	}

	ctx := context.Background()
	class := providerkit.ClassProduction
	stood := vps.NewProvider(vps.Options{
		SSH:       vps.Target{Host: vm.addr, User: vm.user, IdentityFile: vm.key, Config: vm.config},
		DeployKey: named,
	})
	bootstrapper, err := stood.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	closing(t, stood)

	if err := os.Remove(named); err != nil {
		t.Fatal(err)
	}

	p := vps.NewProvider(vps.Options{
		SSH:       vps.Target{Host: vm.addr, User: vm.user, IdentityFile: vm.key, Config: vm.config},
		DeployKey: named,
	})
	defer closing(t, p)
	forgetting, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	if _, err := forgetting.PlanRemoval(ctx, class); err != nil {
		t.Fatalf("PlanRemoval() with the deploy key gone = %v, want a destroy that needs no key to say what it will take", err)
	}
	if err := forgetting.Remove(ctx, class, nil); err != nil {
		t.Fatalf("Remove() with the deploy key gone = %v, want a host nobody can bootstrap to still be one ocel can leave", err)
	}
	if left := strings.TrimSpace(vm.ssh(t, "getent passwd "+deployLogin+" || true")); left != "" {
		t.Errorf("%s still stands as %q after a keyless Remove()", deployLogin, left)
	}
}
