package vps_test

import (
	"context"
	"encoding/base64"
	"os/exec"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const sealed = "postgres://example"

func (vm machine) fed(login, stdin, command string) (string, error) {
	cmd := exec.Command("ssh",
		"-F", vm.config, "-i", vm.key,
		"-o", "IdentitiesOnly=yes", "-o", "BatchMode=yes",
		login+"@"+vm.addr, command)
	cmd.Stdin = strings.NewReader(stdin)
	rendered, err := cmd.Output()
	return string(rendered), err
}

func bootstrapped(t *testing.T, vm machine, class providerkit.Class) *vps.Provider {
	t.Helper()
	p := vm.provider(t)
	t.Cleanup(func() { closing(t, p) })

	bootstrapper, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply(%s) = %v", class, err)
	}
	t.Cleanup(func() {
		if err := bootstrapper.Remove(ctx, class, nil); err != nil {
			t.Errorf("Remove(%s) = %v", class, err)
		}
	})
	return p
}

func sealArgs(class providerkit.Class, verb, name string) string {
	return "sudo -n " + host.SealHelper + " " + string(class) + " " + verb +
		" --project shop --env '*' --folder / --name " + name
}

func TestLiveTheSealKeyIsRootsAloneAndTheDeployLoginNeverReadsIt(t *testing.T) {
	vm := live(t)
	class := providerkit.ClassProduction
	bootstrapped(t, vm, class)

	key := host.SealKeyPath(class)
	if posture := strings.TrimSpace(vm.ssh(t, "sudo stat -c '%a %U %s' "+key)); posture != "400 root 32" {
		t.Errorf("%s stands as %q, want `400 root 32`: 32 bytes of this machine's own randomness, readable by root and nothing beside", key, posture)
	}
	if rendered, err := vm.attempt(deployLogin, "cat "+key); err == nil {
		t.Errorf("%s read %s and got %q, so every value on this host is sealed to bytes the deploy login holds", deployLogin, key, rendered)
	}
	if rendered, err := vm.attempt(deployLogin, "sudo -n cat "+key); err == nil {
		t.Errorf("%s read %s through sudo and got %q, so the sudoers line grants more than the helper", deployLogin, key, rendered)
	}
}

func TestLiveTheDeployLoginSealsAndOpensThroughTheHelperItIsWhitelistedOn(t *testing.T) {
	vm := live(t)
	class := providerkit.ClassProduction
	bootstrapped(t, vm, class)

	fed := base64.StdEncoding.EncodeToString([]byte(sealed))
	rendered, err := vm.fed(deployLogin, fed, sealArgs(class, "seal", "DATABASE_URL"))
	if err != nil {
		t.Fatalf("%s sealed nothing through the helper it is whitelisted on: %v", deployLogin, err)
	}
	if strings.Contains(rendered, fed) {
		t.Fatal("the helper answered a seal carrying the value it was handed")
	}

	opened, err := vm.fed(deployLogin, rendered, sealArgs(class, "open", "DATABASE_URL"))
	if err != nil {
		t.Fatalf("%s could not open what it sealed: %v", deployLogin, err)
	}
	raw, err := base64.StdEncoding.DecodeString(strings.TrimSpace(opened))
	if err != nil {
		t.Fatalf("the helper answered %q, which is no value it sealed", opened)
	}
	if string(raw) != sealed {
		t.Errorf("the round trip answered %q, want %q", raw, sealed)
	}

	if moved, err := vm.fed(deployLogin, rendered, sealArgs(class, "open", "API_KEY")); err == nil {
		t.Errorf("a value sealed at DATABASE_URL opened at API_KEY as %q, so the coordinate authenticates nothing", moved)
	}
}

func TestLiveASealKeyThatWasReplacedIsDriftInStatus(t *testing.T) {
	vm := live(t)
	class := providerkit.ClassProduction
	p := bootstrapped(t, vm, class)

	ctx := context.Background()
	bootstrapper, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	standing, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatal(err)
	}
	if !standing.Stacks[0].DigestCurrent {
		t.Fatal("Describe() after an apply that finished is not current, so nothing below is about the key")
	}

	key := host.SealKeyPath(class)
	if _, err := vm.attempt(deployLogin, "sudo -n "+host.SealHelper+" "+string(class)+" init"); err == nil {
		t.Errorf("a second init over a standing key exited 0, and every value sealed to %s went with it", key)
	}

	vm.ssh(t, "sudo rm -f "+key)
	vm.ssh(t, "sudo "+host.SealHelper+" "+string(class)+" init")

	replaced, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatalf("Describe() over a replaced key = %v", err)
	}
	if replaced.Stacks[0].DigestCurrent {
		t.Error("Describe() calls a host whose seal key was replaced current, so drift in what every secret opens to is invisible")
	}
	if !strings.Contains(vm.ssh(t, "sudo cat "+host.StampPath(class)), host.SealAlgorithm) {
		t.Errorf("the stamp says nothing about how a value is sealed, and %q is what the record claims", host.SealAlgorithm)
	}
}
