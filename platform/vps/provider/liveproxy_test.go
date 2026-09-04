package vps_test

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
)

const adminPort = "2019"

func (vm machine) inspects(t *testing.T, what, name, format string) string {
	t.Helper()
	return strings.TrimSpace(vm.ssh(t, "sudo docker "+what+" inspect -f "+quote(format)+" "+quote(name)+" 2>/dev/null || true"))
}

func (vm machine) inside(t *testing.T, command string) string {
	t.Helper()
	return vm.ssh(t, "sudo docker exec "+host.ProxyContainer+" sh -c "+quote(command)+" 2>&1 || true")
}

func (vm machine) drives(t *testing.T, argv string) string {
	t.Helper()
	return strings.TrimSpace(vm.ssh(t, "sudo docker exec "+host.ProxyContainer+" "+host.ProxyHelperMount+" "+argv+" 2>&1 || true"))
}

func (vm machine) peers(t *testing.T, command string) string {
	t.Helper()
	return vm.ssh(t, "sudo docker run --rm --network "+quote(host.ProxyNetwork)+" "+quote(host.ProxyImage)+
		" sh -c "+quote(command)+" 2>&1 || true")
}

func quote(arg string) string { return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'" }

func TestLiveTheProxyStandsAsStateTheBoxHoldsAndIsWrittenBackWhenItIsGone(t *testing.T) {
	vm := live(t)
	vm.purges(t)
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

	if !vm.running(t, host.ProxyContainer) {
		t.Fatalf("%s is not running after a bootstrap that installs it:\n%s",
			host.ProxyContainer, vm.ssh(t, "sudo docker logs --tail 40 "+host.ProxyContainer+" 2>&1 || true"))
	}
	if image := vm.inspects(t, "container", host.ProxyContainer, "{{.Config.Image}}"); image != host.ProxyImage {
		t.Errorf("the proxy runs %q, want %q: a tag is a name its owner can repoint under a host that already trusts it", image, host.ProxyImage)
	}
	if policy := vm.inspects(t, "container", host.ProxyContainer, "{{.HostConfig.RestartPolicy.Name}}"); policy != "unless-stopped" {
		t.Errorf("the proxy is restarted %q, want unless-stopped: a reboot must not be what takes the box's edge down", policy)
	}
	if networks := vm.inspects(t, "container", host.ProxyContainer, "{{range $n, $v := .NetworkSettings.Networks}}{{$n}} {{end}}"); networks != host.ProxyNetwork {
		t.Errorf("the proxy sits on %q, want the one network %q every deploy target resolves across", networks, host.ProxyNetwork)
	}
	if mode := strings.TrimSpace(vm.ssh(t, "sudo stat -c '%a %U' "+quote(host.ProxyData))); mode != "700 root" {
		t.Errorf("%s stands as %q, want 700 root: it holds every private key on this box and the acme account key that issues for all of them", host.ProxyData, mode)
	}

	for _, held := range []string{"/data/caddy", "/data/config/caddy/autosave.json"} {
		if answer := vm.inside(t, "test -e "+quote(held)+" && echo held || echo gone"); !strings.Contains(answer, "held") {
			t.Errorf("%s is not inside the proxy, so what caddy persists is somewhere this test cannot see", held)
		}
	}
	mounts := vm.inspects(t, "container", host.ProxyContainer, "{{range .Mounts}}{{.Type}}:{{.Destination}}:{{.RW}} {{end}}")
	if !strings.Contains(mounts, "bind:/data:true") {
		t.Errorf("the proxy holds /data as %q, want the bootstrap-owned host path: a named volume appears in no removal plan and would leave every private key on this box after a destroy", mounts)
	}
	if strings.Contains(mounts, "bind:/config") {
		t.Errorf("the proxy binds /config from the host, and what caddy autosaves belongs under the one path a destroy takes: %s", mounts)
	}

	socket := vm.inside(t, "stat -c %a:%U "+quote(host.ProxyAdminSocket))
	mode, owner, split := strings.Cut(strings.TrimSpace(socket), ":")
	if !split {
		t.Fatalf("the admin endpoint left no socket at %s: %q", host.ProxyAdminSocket, socket)
	}
	if owner != "root" {
		t.Errorf("%s is owned by %q, want root", host.ProxyAdminSocket, owner)
	}
	if len(mode) != 3 || mode[1] != '0' || mode[2] != '0' {
		t.Errorf("%s stands at %q, want nothing for group or other: the socket's permissions are the whole of its access control", host.ProxyAdminSocket, mode)
	}

	if listening := vm.inside(t, "netstat -ltn"); strings.Contains(listening, ":"+adminPort) {
		t.Errorf("the proxy carries a tcp listener on %s, and binding the admin endpoint anywhere but the socket is the failure this pick exists to avoid:\n%s", adminPort, listening)
	}
	if bound := vm.ssh(t, "ss -ltn 2>/dev/null || netstat -ltn 2>/dev/null || true"); strings.Contains(bound, ":"+adminPort) {
		t.Errorf("something on this host listens on %s, and the admin endpoint binds no port at all:\n%s", adminPort, bound)
	}
	address := vm.inspects(t, "container", host.ProxyContainer, "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}")
	if address == "" {
		t.Fatal("the proxy holds no address on the shared network, so what a peer container can reach cannot be proven here")
	}
	for _, target := range []string{host.ProxyContainer, address} {
		peered := vm.peers(t, "curl -sS -m 5 -o /dev/null -w '%{http_code}' http://"+target+":"+adminPort+"/config/")
		if strings.Contains(peered, "200") {
			t.Errorf("a container on the shared network reached the admin endpoint at %s:%s and got %q: every app this box runs would hold arbitrary config replacement of its own edge",
				target, adminPort, strings.TrimSpace(peered))
		}
	}
	if published := vm.inspects(t, "container", host.ProxyContainer, "{{json .HostConfig.PortBindings}}"); strings.Contains(published, adminPort) {
		t.Errorf("the proxy publishes %s, and the control plane's socket never leaves the container: %s", adminPort, published)
	}

	if flight := vm.drives(t, "upstreams"); flight != "[]" {
		t.Errorf("the helper read the proxy's upstreams as %q, want an empty set over the socket: the file bootstrap mounts is what the release loop drives", flight)
	}
	if kind := strings.TrimSpace(vm.ssh(t, "sudo head -c 4 "+host.ProxyHelper+" | od -An -c | tr -d ' '")); !strings.Contains(kind, "ELF") {
		t.Errorf("%s stands as %q, want an elf executable: the image lends the release loop no interpreter and no http client", host.ProxyHelper, kind)
	}
	for _, borrowed := range []string{"curl", "wget"} {
		if answer := vm.inside(t, "command -v "+borrowed+" >/dev/null && echo carried || echo absent"); strings.Contains(answer, "absent") {
			t.Logf("the proxy image carries no %s, which is the dependence the helper exists to remove", borrowed)
		}
	}
	if written := vm.inside(t, "printf x >> "+quote(host.ProxyHelperMount)+" && echo wrote || echo refused"); !strings.Contains(written, "refused") {
		t.Errorf("the helper is writable from inside the proxy, and a file the container can rewrite is one the release loop cannot trust: %q", written)
	}
	if mode := strings.TrimSpace(vm.ssh(t, "stat -c %a:%U:%G "+host.ProxyHelper)); mode != "750:root:root" {
		t.Errorf("%s stands at %q, want 750:root:root: nothing but the login docker exec already runs as reaches it", host.ProxyHelper, mode)
	}

	standing, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatal(err)
	}
	if !standing.Stacks[0].DigestCurrent {
		t.Errorf("Describe() calls a box whose proxy has just been installed drifted, %s\n%s",
			stillMoving(t, bootstrapper, class, standing.Held), vm.proxySaid(t))
	}
	again, err := bootstrapper.Plan(ctx, providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Held: standing.Held})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range onlyGroup(t, again).Changes {
		if change.Action != providerkit.ActionKeep {
			t.Errorf("a re-run over a box whose proxy serves plans %q for %s, and a proxy reinstalled on every run is one nobody dares re-run.\n%s",
				change.Action, change.Name, vm.proxySaid(t))
		}
	}

	vm.ssh(t, "sudo docker rm --force "+host.ProxyContainer)
	if vm.running(t, host.ProxyContainer) {
		t.Fatal("the proxy survived being removed, so healing it cannot be proven here")
	}
	torn, err := bootstrapper.Describe(ctx, class)
	if err != nil {
		t.Fatal(err)
	}
	if torn.Stacks[0].DigestCurrent {
		t.Error("Describe() calls a box whose proxy is gone current, and a proxy nothing notices is one nothing repairs")
	}
	healing := providerkit.BootstrapRequest{Class: class, Writer: "live-suite", Held: torn.Held}
	writing, err := bootstrapper.Plan(ctx, healing)
	if err != nil {
		t.Fatal(err)
	}
	group := onlyGroup(t, writing)
	if back := planFor(group, host.ProxyContainer); back.Action != providerkit.ActionCreate {
		t.Errorf("a box whose proxy was removed plans %q for it, want it written back", back.Action)
	}
	for _, change := range group.Changes {
		if change.Name != host.ProxyContainer && change.Action != providerkit.ActionKeep {
			t.Errorf("removing the proxy re-planned %s as %q, and nothing but the container moved", change.Name, change.Action)
		}
	}
	if err := bootstrapper.Apply(ctx, healing, nil); err != nil {
		t.Fatalf("Apply() over a box whose proxy was removed = %v", err)
	}
	if !vm.running(t, host.ProxyContainer) {
		t.Fatalf("%s is still gone after the run that was meant to write it back:\n%s",
			host.ProxyContainer, vm.ssh(t, "sudo docker logs --tail 40 "+host.ProxyContainer+" 2>&1 || true"))
	}
	if flight := vm.drives(t, "upstreams"); flight != "[]" {
		t.Errorf("the proxy that was written back answers its own socket with %q", flight)
	}
}

func TestLiveTheFileOnTheBoxIsTheConfigTheProxyServes(t *testing.T) {
	vm := live(t)
	vm.purges(t)
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

	seeded := vm.drives(t, "config apps/http/grace_period")
	if seeded == "" || seeded == "null" {
		t.Fatalf("the proxy reports its grace period as %q, so what it serves cannot be read back here", seeded)
	}

	moved := `"9s"`
	if seeded == moved {
		moved = `"11s"`
	}
	vm.ssh(t, "sudo sed -i "+quote("s|"+seeded+"|"+moved+"|")+" "+host.ProxyConfig)
	if written := vm.drives(t, "config apps/http/grace_period"); written != seeded {
		t.Fatalf("the running proxy already reports %s before it was restarted, so the restart is not what this test measures", written)
	}
	vm.ssh(t, "sudo docker restart "+host.ProxyContainer)
	for at := 0; at < 20 && !vm.running(t, host.ProxyContainer); at++ {
		time.Sleep(500 * time.Millisecond)
	}

	if served := vm.drives(t, "config apps/http/grace_period"); served != moved {
		t.Errorf("the file on the box declares %q and the running proxy serves %s: caddy's --resume uses the last autosaved configuration, overriding --config, so a box recreated after a changed config would keep serving the old one while every digest ocel holds says it does not",
			moved, served)
	}

	if flags := vm.inspects(t, "container", host.ProxyContainer, "{{json .Config.Cmd}}"); strings.Contains(flags, "resume") {
		t.Errorf("the proxy is run as %s, and the file every deploy replaces is then read and thrown away", flags)
	}
}

func TestLiveTheProxysConfigIsStatedAndItsLogCarriesNoQueryString(t *testing.T) {
	vm := live(t)
	vm.purges(t)
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

	if owner := strings.TrimSpace(vm.ssh(t, "sudo stat -c %U:%a "+host.ProxyConfig)); owner != deployLogin+":640" {
		t.Errorf("%s stands as %q, want the deploy principal's own file: the config is what a deploy renders", host.ProxyConfig, owner)
	}
	grace := vm.drives(t, "config apps/http/grace_period")
	if grace == "" || grace == "null" {
		t.Errorf("the proxy reports its grace period as %q, and caddy's default is eternal: one hung request would hold a retired server open forever", grace)
	}

	vm.peers(t, "curl -sS -m 5 -o /dev/null -H 'Authorization: Bearer TOPSECRET' "+
		"'http://"+host.ProxyContainer+"/callback?code=TOPSECRET&state=xyz'")
	logged := vm.ssh(t, "sudo docker logs --tail 20 "+host.ProxyContainer+" 2>&1 || true")
	if !strings.Contains(logged, "/callback") {
		t.Fatalf("the proxy logged no request at all, so what its log carries is not a decision:\n%s", logged)
	}
	if strings.Contains(logged, "TOPSECRET") {
		t.Errorf("a secret carried in a query string and a bearer token both reached the proxy's log:\n%s", logged)
	}
	if !strings.Contains(logged, "REDACTED") {
		t.Errorf("the authorization header is logged as itself rather than redacted:\n%s", logged)
	}
}

func TestLiveDestroyTakesOcelsProxyAndLeavesTheContainersTheHostRuns(t *testing.T) {
	vm := live(t)
	vm.purges(t)
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
	vm.runs(t, workload)
	defer vm.ssh(t, "sudo docker rm -f "+workload+" >/dev/null 2>&1 || true")

	removal, err := bootstrapper.PlanRemoval(ctx, class)
	if err != nil {
		t.Fatalf("PlanRemoval() = %v", err)
	}
	leaving := onlyGroup(t, removal)
	for _, taken := range []string{host.ProxyContainer, host.ProxyData, host.ProxyNetwork} {
		planned := planFor(leaving, taken)
		if planned.Action != providerkit.ActionDelete {
			t.Errorf("PlanRemoval() plans %s as %q, want it taken: what ocel wrote is what ocel takes back", taken, planned.Action)
		}
		if planned.Reason == "" {
			t.Errorf("PlanRemoval() takes %s with no reason, and the typed confirmation must name what goes before a user types", taken)
		}
	}

	if err := bootstrapper.Remove(ctx, class, nil); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	if vm.running(t, host.ProxyContainer) {
		t.Errorf("%s still runs after a destroy, and a proxy nobody wrote back is one nobody takes down", host.ProxyContainer)
	}
	if stood := vm.inspects(t, "network", host.ProxyNetwork, "{{.Name}}"); stood != "" {
		t.Errorf("the network %s stands after a destroy", host.ProxyNetwork)
	}
	for _, gone := range []string{host.ProxyHelper, host.ProxyConfig, host.ProxyData} {
		if vm.stands(t, gone) {
			t.Errorf("%s stands after a destroy took the last class on this host", gone)
		}
	}
	if !vm.running(t, workload) {
		t.Errorf("%s is gone after a destroy, and removing ocel took a container ocel never ran", workload)
	}
	if active := strings.TrimSpace(vm.ssh(t, "systemctl is-active docker.service || true")); active != "active" {
		t.Errorf("docker.service is %q after a destroy, want a daemon that still serves this host's workloads", active)
	}
}
