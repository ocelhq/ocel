package vps_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	vps "github.com/ocelhq/ocel/platform/vps/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const sealed = "postgres://example"

func (vm machine) deploying(t *testing.T) *vps.Provider {
	t.Helper()
	p := vps.NewProvider(vps.Options{SSH: vps.Target{
		Host:         vm.addr,
		User:         deployLogin,
		IdentityFile: vm.key,
		Config:       vm.config,
	}})
	t.Cleanup(func() { closing(t, p) })
	return p
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

func sealedAt(class providerkit.Class, name string) providerkit.Coordinate {
	return providerkit.Coordinate{Project: "shop", Class: class, Env: "*", Folder: "/", Name: name}
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

	ctx := context.Background()
	sealer := vm.deploying(t).Sealer()
	at := sealedAt(class, "DATABASE_URL")

	written, err := sealer.Seal(ctx, at, []byte(sealed))
	if err != nil {
		t.Fatalf("%s sealed nothing through the helper it is whitelisted on: %v", deployLogin, err)
	}
	if bytes.Contains(written, []byte(sealed)) {
		t.Fatal("Seal() answered a value carrying the plaintext it was handed")
	}

	opened, err := sealer.Open(ctx, at, written)
	if err != nil {
		t.Fatalf("%s could not open what it sealed: %v", deployLogin, err)
	}
	if string(opened) != sealed {
		t.Errorf("the round trip answered %q, want %q", opened, sealed)
	}

	if moved, err := sealer.Open(ctx, sealedAt(class, "API_KEY"), written); err == nil {
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

	if err := bootstrapper.Apply(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite"}, nil); err == nil {
		t.Error("an apply over a replaced key finished, and the stamp now records a key that opens nothing this class ever sealed")
	}
}
