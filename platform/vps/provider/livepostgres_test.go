package vps_test

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

func (vm machine) queries(t *testing.T, binding providerkit.Binding, statement string) string {
	t.Helper()
	held := binding.Properties
	image := strings.TrimSpace(vm.ssh(t, "sudo docker inspect -f '{{.Config.Image}}' "+quote(held[providerkit.PropertyHost])))
	return strings.TrimSpace(vm.ssh(t, "sudo docker run --rm --network "+quote(host.AppNetwork(providerkit.ClassProduction, "shop"))+
		" --env "+quote("PGPASSWORD="+held[providerkit.PropertyPassword])+" "+quote(image)+
		" psql -h "+quote(held[providerkit.PropertyHost])+" -p "+quote(held[providerkit.PropertyPort])+
		" -U "+quote(held[providerkit.PropertyUsername])+" -d "+quote(held[providerkit.PropertyDatabase])+
		" -tA -v ON_ERROR_STOP=1 -c "+quote(statement)+" 2>&1 || true"))
}

func TestLiveADeclaredPostgresAnswersItsProjectAndNothingElseAndLeavesNothingBehind(t *testing.T) {
	vm := live(t)
	bootstrapped(t, vm, providerkit.ClassProduction)
	p := vm.deploying(t)
	ctx := context.Background()
	in := aPostgres(t, "16")
	t.Cleanup(func() {
		vm.ssh(t, "sudo docker ps -aq --filter label="+host.LabelResource+" | xargs -r sudo docker rm -f >/dev/null 2>&1 || true")
		vm.ssh(t, "sudo docker volume ls -q --filter label="+host.LabelResource+" | xargs -r sudo docker volume rm >/dev/null 2>&1 || true")
	})

	binding, err := p.Postgres(ctx, in, nil)
	if err != nil {
		t.Fatalf("Postgres() = %v", err)
	}
	name := binding.Properties[providerkit.PropertyHost]
	if said := vm.queries(t, binding, "CREATE TABLE kept (id int); INSERT INTO kept VALUES (7); SELECT id FROM kept"); !strings.HasSuffix(said, "7") {
		t.Fatalf("a client on the project's network bound to what came back and was answered %q", said)
	}
	if published := strings.TrimSpace(vm.ssh(t, "sudo docker port "+quote(name))); published != "" {
		t.Errorf("the server publishes %q on the box, and a database the machine's own address reaches is one the internet does", published)
	}
	kept := strings.TrimSpace(vm.ssh(t, "sudo cat "+quote(host.KeptPath(providerkit.ClassProduction, name))))
	if kept == "" || strings.Contains(kept, binding.Properties[providerkit.PropertyPassword]) {
		t.Errorf("the box keeps %d bytes for %s, and what it keeps is the password itself or nothing", len(kept), name)
	}

	started := vm.ssh(t, "sudo docker inspect -f '{{.Id}} {{.State.StartedAt}}' "+quote(name))
	again, err := p.Postgres(ctx, in, nil)
	if err != nil {
		t.Fatalf("a second Postgres() = %v", err)
	}
	if again.Properties[providerkit.PropertyPassword] != binding.Properties[providerkit.PropertyPassword] {
		t.Error("a second deploy bound to another password, and every app the first one bound is locked out")
	}
	if now := vm.ssh(t, "sudo docker inspect -f '{{.Id}} {{.State.StartedAt}}' "+quote(name)); now != started {
		t.Errorf("a second deploy of an unchanged postgres moved it from %q to %q, and every connection it held was dropped for nothing", started, now)
	}
	if said := vm.queries(t, again, "SELECT id FROM kept"); said != "7" {
		t.Errorf("the data a second deploy finds is %q, want the row the first one wrote", said)
	}

	dumped := strings.TrimSpace(vm.ssh(t, "sudo "+host.BackupsHelper+" production dump "+quote(name)+" main"))
	if !vm.stands(t, dumped) {
		t.Fatalf("the helper said it dumped %s to %q and nothing stands there", name, dumped)
	}
	if armed := strings.TrimSpace(vm.ssh(t, "systemctl is-enabled "+host.BackupsTimer+" || true")); armed != "enabled" {
		t.Errorf("the daily dump is %q on a bootstrapped box, and a database nothing dumps is one disk away from gone", armed)
	}

	upgraded, err := p.Postgres(ctx, aPostgres(t, "17"), nil)
	if err != nil {
		t.Fatalf("moving %s from 16 to 17 = %v", name, err)
	}
	if said := vm.queries(t, upgraded, "SELECT id FROM kept"); said != "7" {
		t.Errorf("the data after the move to 17 is %q, want the row version 16 held", said)
	}
	if said := vm.queries(t, upgraded, "SHOW server_version_num"); !strings.HasPrefix(said, "17") {
		t.Errorf("the server answers as version %q after the move to 17", said)
	}
	if left := strings.TrimSpace(vm.ssh(t, "sudo docker volume ls -q --filter name="+quote("^"+name+"-g16$"))); left != "" {
		t.Errorf("version 16's volume %q outlives the move, and nothing after this reclaims it", left)
	}

	if err := p.RemoveResource(ctx, in.Ref, again, nil); err != nil {
		t.Fatalf("RemoveResource() = %v", err)
	}
	if vm.running(t, name) {
		t.Errorf("%s still runs after its stack was removed", name)
	}
	if volumes := strings.TrimSpace(vm.ssh(t, "sudo docker volume ls -q --filter name="+quote("^"+name))); volumes != "" {
		t.Errorf("the volume %q outlives the stack that declared it, and nothing after this reclaims the disk", volumes)
	}
	if vm.stands(t, host.BackupsDir(providerkit.ClassProduction, name)) {
		t.Errorf("the dumps taken of %s outlive the stack that declared it", name)
	}
	if vm.stands(t, host.KeptPath(providerkit.ClassProduction, name)) {
		t.Errorf("the sealed password for %s outlives the server it opened", name)
	}
}
