package vps_test

import (
	"bytes"
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
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

func bootstrapped(t *testing.T, vm machine, class edge.Class) *vps.Provider {
	t.Helper()
	p := vm.provider(t)
	t.Cleanup(func() { closing(t, p) })

	bootstrap, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	described, err := bootstrap.Describe(ctx, class)
	if err != nil {
		t.Fatalf("Describe(%s) = %v", class, err)
	}
	if described.Present && !described.Unfinished && described.Stacks[0].DigestCurrent {
		return p
	}
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Class: class, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply(%s) = %v", class, err)
	}
	return p
}

func dirties(t *testing.T, vm machine) {
	t.Helper()
	t.Cleanup(func() {
		vm.forgetsTheDeployLogin(t)
		vm.purges(t)
	})
}

func sealedAt(class edge.Class, name string) records.SealScope {
	return records.SealScope{Project: "shop", Class: class, Env: "*", Folder: "/", Name: name}
}

func TestLiveTheSealKeyIsRootsAloneAndTheDeployLoginNeverReadsIt(t *testing.T) {
	vm := liveMachine(t)
	class := edge.ClassProduction
	bootstrapped(t, vm, class)

	key := host.SealKeyPath(class)
	if posture := strings.TrimSpace(vm.ssh(t, "sudo stat -c '%a %U %s' "+key)); posture != "400 root 32" {
		t.Errorf("%s is %q, want `400 root 32`: 32 bytes of this machine's own randomness, readable by root and nothing beside", key, posture)
	}
	if rendered, err := vm.attempt(deployLogin, "cat "+key); err == nil {
		t.Errorf("%s read %s and got %q, so every value on this host is sealed to bytes the deploy login can read", deployLogin, key, rendered)
	}
	if rendered, err := vm.attempt(deployLogin, "sudo -n cat "+key); err == nil {
		t.Errorf("%s read %s through sudo and got %q, so the sudoers line grants more than the helper", deployLogin, key, rendered)
	}
}

func TestLiveTheDeployLoginSealsAndOpensThroughTheHelperItIsWhitelistedOn(t *testing.T) {
	vm := liveMachine(t)
	class := edge.ClassProduction
	bootstrapped(t, vm, class)

	ctx := context.Background()
	cipher := vm.deploying(t).Cipher()
	at := sealedAt(class, "DATABASE_URL")

	written, err := cipher.Seal(ctx, at, []byte(sealed))
	if err != nil {
		t.Fatalf("%s sealed nothing through the helper it is whitelisted on: %v", deployLogin, err)
	}
	if bytes.Contains(written, []byte(sealed)) {
		t.Fatal("Seal() answered a value containing the plaintext it was handed")
	}

	opened, err := cipher.Open(ctx, at, written)
	if err != nil {
		t.Fatalf("%s could not open what it sealed: %v", deployLogin, err)
	}
	if string(opened) != sealed {
		t.Errorf("the round trip answered %q, want %q", opened, sealed)
	}

	if moved, err := cipher.Open(ctx, sealedAt(class, "API_KEY"), written); err == nil {
		t.Errorf("a value sealed at DATABASE_URL opened at API_KEY as %q, so the coordinate authenticates nothing", moved)
	}
}

func TestLiveASealKeyThatWasReplacedIsDriftInStatus(t *testing.T) {
	vm := liveMachine(t)
	class := edge.ClassProduction
	p := bootstrapped(t, vm, class)
	dirties(t, vm)

	ctx := context.Background()
	bootstrap, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	described, err := bootstrap.Describe(ctx, class)
	if err != nil {
		t.Fatal(err)
	}
	if !described.Stacks[0].DigestCurrent {
		t.Fatal("Describe() after an apply that finished is not current, so nothing below is about the key")
	}

	key := host.SealKeyPath(class)
	if _, err := vm.attempt(deployLogin, "sudo -n "+host.SealHelper+" "+string(class)+" init"); err == nil {
		t.Errorf("a second init over an existing key exited 0, and every value sealed to %s went with it", key)
	}

	vm.ssh(t, "sudo rm -f "+key)
	vm.ssh(t, "sudo "+host.SealHelper+" "+string(class)+" init")

	replaced, err := bootstrap.Describe(ctx, class)
	if err != nil {
		t.Fatalf("Describe() over a replaced key = %v", err)
	}
	if replaced.Stacks[0].DigestCurrent {
		t.Error("Describe() calls a host whose seal key was replaced current, so drift in what every secret opens to is invisible")
	}
	if !strings.Contains(vm.ssh(t, "sudo cat "+host.StampPath(class)), host.SealAlgorithm) {
		t.Errorf("the stamp says nothing about how a value is sealed, and %q is what the record claims", host.SealAlgorithm)
	}

	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Class: class, WrittenBy: "live-suite"}, nil); err == nil {
		t.Error("an apply over a replaced key finished, and the stamp now records a key that opens nothing this class ever sealed")
	}
}
