//go:build integration

package vps_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

func (vm machine) commandsStore(t *testing.T, binding provider.Binding, password string, command ...string) string {
	t.Helper()
	props := binding.Properties
	image := strings.TrimSpace(vm.ssh(t, "sudo docker inspect -f '{{.Config.Image}}' "+quote(props[provider.PropertyHost])))
	argv := []string{"sudo", "docker", "run", "--rm", "--network", quote(live.AppNetwork(environment.TierProduction, "shop")),
		"--entrypoint", "valkey-cli", quote(image),
		"-h", quote(props[provider.PropertyHost]), "-p", quote(props[provider.PropertyPort]), "--no-auth-warning"}
	if password != "" {
		argv = append(argv, "--user", quote(props[provider.PropertyUsername]), "-a", quote(password))
	}
	for _, word := range command {
		argv = append(argv, quote(word))
	}
	return strings.TrimSpace(vm.ssh(t, strings.Join(argv, " ")+" 2>&1 || true"))
}

func TestLiveADeclaredKVStoreAnswersOnlyItsUserOnItsProjectAndLeavesNothingBehind(t *testing.T) {
	vm := liveMachine(t)
	bootstrapped(t, vm, environment.TierProduction)
	p := vm.deploying(t)
	ctx := context.Background()
	in := aKVStore(t, "8")
	t.Cleanup(func() {
		vm.ssh(t, "sudo docker ps -aq --filter label="+host.LabelResource+" | xargs -r sudo docker rm -f >/dev/null 2>&1 || true")
		vm.ssh(t, "sudo docker volume ls -q --filter label="+host.LabelResource+" | xargs -r sudo docker volume rm >/dev/null 2>&1 || true")
	})

	binding, err := p.ProvisionKV(ctx, in, nil)
	if err != nil {
		t.Fatalf("ProvisionKV() = %v", err)
	}
	name, password := binding.Properties[provider.PropertyHost], binding.Properties[provider.PropertyPassword]
	if said := vm.commandsStore(t, binding, "", "PING"); !strings.Contains(said, "NOAUTH") {
		t.Errorf("an unauthenticated client was answered %q, want the default user off", said)
	}
	if said := vm.commandsStore(t, binding, password, "SET", "kept", "7"); said != "OK" {
		t.Fatalf("the bound user's SET was answered %q", said)
	}
	if published := strings.TrimSpace(vm.ssh(t, "sudo docker port "+quote(name))); published != "" {
		t.Errorf("the store publishes %q on the box", published)
	}
	if inspected := vm.ssh(t, "sudo docker inspect "+quote(name)); strings.Contains(inspected, password) {
		t.Error("docker inspect shows the store's password")
	}
	kept := strings.TrimSpace(vm.ssh(t, "sudo cat "+quote(host.KeptPath(environment.TierProduction, name))))
	if kept == "" || strings.Contains(kept, password) {
		t.Errorf("the box keeps %d bytes for %s, and what it keeps is the password itself or nothing", len(kept), name)
	}

	started := vm.ssh(t, "sudo docker inspect -f '{{.Id}} {{.State.StartedAt}}' "+quote(name))
	again, err := p.ProvisionKV(ctx, in, nil)
	if err != nil {
		t.Fatalf("a second ProvisionKV() = %v", err)
	}
	if again.Properties[provider.PropertyPassword] != password {
		t.Error("a second deploy bound to another password")
	}
	if now := vm.ssh(t, "sudo docker inspect -f '{{.Id}} {{.State.StartedAt}}' "+quote(name)); now != started {
		t.Errorf("a second deploy of an unchanged store moved it from %q to %q", started, now)
	}

	vm.ssh(t, "sudo docker restart "+quote(name)+" >/dev/null")
	if said := vm.commandsStore(t, again, password, "GET", "kept"); said != "7" {
		t.Errorf("after a restart the store answers %q, want the value written before it", said)
	}

	upgraded, err := p.ProvisionKV(ctx, aKVStore(t, "9"), nil)
	if err != nil {
		t.Fatalf("moving %s from 8 to 9 = %v", name, err)
	}
	if said := vm.commandsStore(t, upgraded, password, "GET", "kept"); said != "7" {
		t.Errorf("the data after the move to 9 is %q, want the value version 8 stored", said)
	}
	if left := strings.TrimSpace(vm.ssh(t, "sudo docker volume ls -q --filter name="+quote("^"+name+"-g8$"))); left != "" {
		t.Errorf("version 8's volume %q outlives the move", left)
	}

	if err := p.RemoveResource(ctx, in.Ref, upgraded, nil); err != nil {
		t.Fatalf("RemoveResource() = %v", err)
	}
	if vm.running(t, name) {
		t.Errorf("%s still runs after its stack was removed", name)
	}
	if volumes := strings.TrimSpace(vm.ssh(t, "sudo docker volume ls -q --filter name="+quote("^"+name))); volumes != "" {
		t.Errorf("the volume %q outlives the stack that declared it", volumes)
	}
	if vm.exists(t, host.KeptPath(environment.TierProduction, name)) {
		t.Errorf("the sealed password for %s outlives the store it opened", name)
	}
}
