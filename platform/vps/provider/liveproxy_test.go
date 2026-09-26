package vps_test

import (
	"context"
	"encoding/json"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/host"
	vars "github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

var adminPort = strconv.Itoa(caddy.AdminPort)

func (vm machine) inspects(t *testing.T, what, name, format string) string {
	t.Helper()
	rendered, err := vm.attempt(vm.user, "sudo docker "+what+" inspect -f "+quote(format)+" "+quote(name))
	if err != nil {
		if absent(err) {
			return ""
		}
		t.Fatalf("docker %s inspect -f %s %s: %v", what, quote(format), quote(name), err)
	}
	return strings.TrimSpace(rendered)
}

var (
	noSuchObject   = regexp.MustCompile(`no such (container|image|object|volume|network|plugin|config|secret)\b`)
	objectNotFound = regexp.MustCompile(`\b(container|image|object|volume|network|plugin|config|secret) \S+ not found\b`)
)

func absent(said error) bool {
	lowered := strings.ToLower(said.Error())
	return noSuchObject.MatchString(lowered) || objectNotFound.MatchString(lowered)
}

const containerSaid = "ocel-container-said"

func containerScript(command string) string {
	return quote("{\n" + command + "\n} 2>&1\nprintf " + quote("\n"+containerSaid))
}

func spoken(rendered string) (string, bool) {
	said, ran := strings.CutSuffix(rendered, containerSaid)
	if !ran {
		return "", false
	}
	return strings.TrimSuffix(said, "\n"), true
}

func (vm machine) ran(t *testing.T, what, command string) string {
	t.Helper()
	rendered, err := vm.attempt(vm.user, command)
	said, ran := spoken(rendered)
	if !ran {
		t.Fatalf("%s never ran, so what it would have said is not what this reads: %v\n%s", what, err, rendered)
	}
	return said
}

func (vm machine) inside(t *testing.T, command string) string {
	t.Helper()
	return vm.ran(t, "a command in "+caddy.Container,
		"sudo docker exec "+caddy.Container+" sh -c "+containerScript(command))
}

func (vm machine) drives(t *testing.T, argv string) string {
	t.Helper()
	return strings.TrimSpace(vm.ran(t, host.SwitchboardMounted+" in "+host.SwitchboardContainer,
		"sudo sh -c "+containerScript("docker exec "+host.SwitchboardContainer+" "+host.SwitchboardMounted+" "+argv)))
}

func (vm machine) frontGrace(t *testing.T) string {
	t.Helper()
	saved := vm.ssh(t, "sudo cat "+quote(host.ProxyData+"/config/caddy/autosave.json")+" 2>/dev/null || true")
	var running struct {
		Apps struct {
			HTTP struct {
				GracePeriod json.RawMessage `json:"grace_period"`
			} `json:"http"`
		} `json:"apps"`
	}
	if err := json.Unmarshal([]byte(saved), &running); err != nil {
		return ""
	}
	return string(running.Apps.HTTP.GracePeriod)
}

func (vm machine) peers(t *testing.T, command string) string {
	t.Helper()
	return vm.ran(t, "a container on "+host.ProxyNetwork,
		"sudo docker run --rm --network "+quote(host.ProxyNetwork)+" "+quote(caddy.Image)+
			" sh -c "+containerScript(command))
}

func (vm machine) beside(t *testing.T, container, command string) string {
	t.Helper()
	return vm.ran(t, "a container sharing the network of "+container,
		"sudo docker run --rm --network "+quote("container:"+container)+" "+quote(caddy.Image)+
			" sh -c "+containerScript(command))
}

func quote(arg string) string { return "'" + strings.ReplaceAll(arg, "'", `'\''`) + "'" }

func TestLiveTheProxyStandsAsStateTheBoxHoldsAndIsWrittenBackWhenItIsGone(t *testing.T) {
	vm := liveMachine(t)
	vm.purges(t)
	p := vm.provider(t)
	defer closing(t, p)

	ctx := context.Background()
	class := edge.ClassProduction
	bootstrap, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Class: class, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	defer func() {
		if err := bootstrap.Remove(ctx, class, nil); err != nil {
			t.Errorf("Remove() = %v", err)
		}
	}()

	if !vm.running(t, caddy.Container) {
		t.Fatalf("%s is not running after a bootstrap that installs it:\n%s",
			caddy.Container, vm.ssh(t, "sudo docker logs --tail 40 "+caddy.Container+" 2>&1 || true"))
	}
	if image := vm.inspects(t, "container", caddy.Container, "{{.Config.Image}}"); image != caddy.Image {
		t.Errorf("the proxy runs %q, want %q: a tag is a name its owner can repoint under a host that already trusts it", image, caddy.Image)
	}
	if policy := vm.inspects(t, "container", caddy.Container, "{{.HostConfig.RestartPolicy.Name}}"); policy != "unless-stopped" {
		t.Errorf("the proxy is restarted %q, want unless-stopped: a reboot must not be what takes the box's edge down", policy)
	}
	if networks := vm.inspects(t, "container", caddy.Container, "{{range $n, $v := .NetworkSettings.Networks}}{{$n}} {{end}}"); !slices.Equal(strings.Fields(networks), []string{host.ProxyNetwork}) {
		t.Errorf("the proxy sits on %q, want %q alone: it forwards to the switchboard and reaches no app", networks, host.ProxyNetwork)
	}
	if !vm.running(t, host.SwitchboardContainer) {
		t.Fatalf("%s is not running after a bootstrap that installs it:\n%s",
			host.SwitchboardContainer, vm.ssh(t, "sudo docker logs --tail 40 "+host.SwitchboardContainer+" 2>&1 || true"))
	}
	if mode := strings.TrimSpace(vm.ssh(t, "sudo stat -c '%a %U' "+quote(host.ProxyData))); mode != "700 root" {
		t.Errorf("%s stands as %q, want 700 root: it holds every private key on this box and the acme account key that issues for all of them", host.ProxyData, mode)
	}

	for _, held := range []string{"/data/caddy", "/data/config/caddy/autosave.json"} {
		if answer := vm.inside(t, "test -e "+quote(held)+" && echo held || echo gone"); !strings.Contains(answer, "held") {
			t.Errorf("%s is not inside the proxy, so what caddy persists is somewhere this test cannot see", held)
		}
	}
	mounts := vm.inspects(t, "container", caddy.Container, "{{range .Mounts}}{{.Type}}:{{.Destination}}:{{.RW}} {{end}}")
	if !strings.Contains(mounts, "bind:/data:true") {
		t.Errorf("the proxy holds /data as %q, want the bootstrap-owned host path: a named volume appears in no removal plan and would leave every private key on this box after a destroy", mounts)
	}
	if strings.Contains(mounts, "bind:/config") {
		t.Errorf("the proxy binds /config from the host, and what caddy autosaves belongs under the one path a destroy takes: %s", mounts)
	}

	socket := vm.inside(t, "stat -c %a:%U "+quote(caddy.AdminSocket))
	mode, owner, split := strings.Cut(strings.TrimSpace(socket), ":")
	if !split {
		t.Fatalf("the admin endpoint left no socket at %s: %q", caddy.AdminSocket, socket)
	}
	if owner != "root" {
		t.Errorf("%s is owned by %q, want root", caddy.AdminSocket, owner)
	}
	if len(mode) != 3 || mode[1] != '0' || mode[2] != '0' {
		t.Errorf("%s stands at %q, want nothing for group or other: the socket's permissions are the whole of its access control", caddy.AdminSocket, mode)
	}

	listening := vm.inside(t, "command -v netstat >/dev/null || echo no-netstat\nnetstat -ltn")
	if strings.Contains(listening, "no-netstat") {
		t.Fatalf("the proxy image carries no netstat, and a listing nothing produced carries no port to find:\n%s", listening)
	}
	if strings.Contains(listening, ":"+adminPort) {
		t.Errorf("the proxy carries a tcp listener on %s, and binding the admin endpoint anywhere but the socket is the failure this pick exists to avoid:\n%s", adminPort, listening)
	}
	if bound := vm.ssh(t, "ss -ltn 2>/dev/null || netstat -ltn 2>/dev/null || true"); strings.Contains(bound, ":"+adminPort) {
		t.Errorf("something on this host listens on %s, and the admin endpoint binds no port at all:\n%s", adminPort, bound)
	}
	address := vm.inspects(t, "container", caddy.Container, "{{range .NetworkSettings.Networks}}{{.IPAddress}}{{end}}")
	if address == "" {
		t.Fatal("the proxy holds no address on the shared network, so what a peer container can reach cannot be proven here")
	}
	for _, target := range []string{caddy.Container, address} {
		peered := vm.peers(t, "curl -sS -m 5 -o /dev/null -w '%{http_code}' http://"+target+":"+adminPort+"/config/")
		if code := strings.TrimSpace(peered); len(code) >= 3 && code[len(code)-3:] == "200" {
			t.Errorf("a container on the shared network reached the admin endpoint at %s:%s and got %q: every app this box runs would hold arbitrary config replacement of its own edge",
				target, adminPort, strings.TrimSpace(peered))
		}
	}
	if published := vm.inspects(t, "container", caddy.Container, "{{json .HostConfig.PortBindings}}"); strings.Contains(published, adminPort) {
		t.Errorf("the proxy publishes %s, and the control plane's socket never leaves the container: %s", adminPort, published)
	}

	if flight := vm.drives(t, "upstreams"); flight != "[]" {
		t.Errorf("the switchboard read its upstreams as %q, want an empty set over its control socket: the binary bootstrap mounts is what the release loop drives", flight)
	}
	if kind := strings.TrimSpace(vm.ssh(t, "sudo head -c 4 "+host.SwitchboardBinary+" | od -An -c | tr -d ' '")); !strings.Contains(kind, "ELF") {
		t.Errorf("%s stands as %q, want an elf executable: the image it runs in lends it no interpreter", host.SwitchboardBinary, kind)
	}
	if mode := strings.TrimSpace(vm.ssh(t, "stat -c %a:%U:%G "+host.SwitchboardBinary)); mode != "755:root:root" {
		t.Errorf("%s stands at %q, want 755:root:root: root alone writes what routes every hostname, and the deploy login runs it to read what the box serves", host.SwitchboardBinary, mode)
	}
	board := vm.inspects(t, "container", host.SwitchboardContainer, "{{range .Mounts}}{{.Type}}:{{.Destination}}:{{.RW}} {{end}}")
	for _, held := range []string{"bind:/ocel/switchboard:false", "bind:" + vars.RoutingDir + ":false"} {
		if !strings.Contains(board, held) {
			t.Errorf("the switchboard mounts %q, want %s among them: a directory it reads and cannot write", board, held)
		}
	}
	if published := vm.inspects(t, "container", host.SwitchboardContainer, "{{json .HostConfig.PortBindings}}"); published != "{}" && published != "null" {
		t.Errorf("the switchboard publishes %s, and nothing but the front proxy is reached from off the box", published)
	}

	standing, err := bootstrap.Describe(ctx, class)
	if err != nil {
		t.Fatal(err)
	}
	if !standing.Stacks[0].DigestCurrent {
		t.Errorf("Describe() calls a box whose proxy has just been installed drifted, %s\n%s",
			stillMoving(t, bootstrap, class, standing.VendorState), vm.proxySaid(t))
	}
	again, err := bootstrap.Plan(ctx, provider.BootstrapRequest{Class: class, WrittenBy: "live-suite", VendorState: standing.VendorState})
	if err != nil {
		t.Fatal(err)
	}
	for _, change := range onlyGroup(t, again).Changes {
		if change.Action.Writes() {
			t.Errorf("a re-run over a box whose proxy serves plans %q for %s, and a proxy reinstalled on every run is one nobody dares re-run.\n%s",
				change.Action, change.Name, vm.proxySaid(t))
		}
	}

	vm.ssh(t, "sudo docker rm --force "+caddy.Container)
	if vm.running(t, caddy.Container) {
		t.Fatal("the proxy survived being removed, so healing it cannot be proven here")
	}
	torn, err := bootstrap.Describe(ctx, class)
	if err != nil {
		t.Fatal(err)
	}
	if torn.Stacks[0].DigestCurrent {
		t.Error("Describe() calls a box whose proxy is gone current, and a proxy nothing notices is one nothing repairs")
	}
	healing := provider.BootstrapRequest{Class: class, WrittenBy: "live-suite", VendorState: torn.VendorState}
	writing, err := bootstrap.Plan(ctx, healing)
	if err != nil {
		t.Fatal(err)
	}
	group := onlyGroup(t, writing)
	if back := planFor(group, caddy.Container); back.Action != provider.ActionCreate {
		t.Errorf("a box whose proxy was removed plans %q for it, want it written back", back.Action)
	}
	for _, change := range group.Changes {
		if change.Name != caddy.Container && change.Action.Writes() {
			t.Errorf("removing the proxy re-planned %s as %q, and nothing but the container moved", change.Name, change.Action)
		}
	}
	if err := bootstrap.Apply(ctx, healing, nil); err != nil {
		t.Fatalf("Apply() over a box whose proxy was removed = %v", err)
	}
	if !vm.running(t, caddy.Container) {
		t.Fatalf("%s is still gone after the run that was meant to write it back:\n%s",
			caddy.Container, vm.ssh(t, "sudo docker logs --tail 40 "+caddy.Container+" 2>&1 || true"))
	}
	if flight := vm.drives(t, "upstreams"); flight != "[]" {
		t.Errorf("the proxy that was written back answers its own socket with %q", flight)
	}
}

func TestLiveTheFileOnTheBoxIsTheConfigTheProxyServes(t *testing.T) {
	vm := liveMachine(t)
	vm.purges(t)
	p := vm.provider(t)
	defer closing(t, p)

	ctx := context.Background()
	class := edge.ClassProduction
	bootstrap, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Class: class, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	defer func() {
		if err := bootstrap.Remove(ctx, class, nil); err != nil {
			t.Errorf("Remove() = %v", err)
		}
	}()

	seeded := vm.frontGrace(t)
	if seeded == "" || seeded == "null" {
		t.Fatalf("the proxy reports its grace period as %q, so what it serves cannot be read back here", seeded)
	}

	moved := `"9s"`
	if seeded == moved {
		moved = `"11s"`
	}
	vm.ssh(t, "sudo sed -i "+quote("s|"+seeded+"|"+moved+"|")+" "+host.ProxyConfig)
	if written := vm.frontGrace(t); written != seeded {
		t.Fatalf("the running proxy already reports %s before it was restarted, so the restart is not what this test measures", written)
	}
	vm.ssh(t, "sudo docker restart "+caddy.Container)
	for at := 0; at < 20 && !vm.running(t, caddy.Container); at++ {
		time.Sleep(500 * time.Millisecond)
	}

	if served := vm.frontGrace(t); served != moved {
		t.Errorf("the file on the box declares %q and the running proxy serves %s: caddy's --resume uses the last autosaved configuration, overriding --config, so a box recreated after a changed config would keep serving the old one while every digest ocel holds says it does not",
			moved, served)
	}

	if flags := vm.inspects(t, "container", caddy.Container, "{{json .Config.Cmd}}"); strings.Contains(flags, "resume") {
		t.Errorf("the proxy is run as %s, and the file every deploy replaces is then read and thrown away", flags)
	}
}

func TestLiveTheProxysConfigIsStatedAndItsLogCarriesNoQueryString(t *testing.T) {
	vm := liveMachine(t)
	vm.purges(t)
	p := vm.provider(t)
	defer closing(t, p)

	ctx := context.Background()
	class := edge.ClassProduction
	bootstrap, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Class: class, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	defer func() {
		if err := bootstrap.Remove(ctx, class, nil); err != nil {
			t.Errorf("Remove() = %v", err)
		}
	}()

	if owner := strings.TrimSpace(vm.ssh(t, "sudo stat -c %U:%a "+host.ProxyConfig)); owner != deployLogin+":640" {
		t.Errorf("%s stands as %q, want the deploy principal's own file: the config is what a deploy renders", host.ProxyConfig, owner)
	}
	if owner := strings.TrimSpace(vm.ssh(t, "sudo stat -c %U:%a "+vars.RoutingTable)); owner != deployLogin+":640" {
		t.Errorf("%s stands as %q, want the deploy principal's own file: the table is what a deploy writes", vars.RoutingTable, owner)
	}
	grace := vm.frontGrace(t)
	if grace == "" || grace == "null" {
		t.Errorf("the proxy reports its grace period as %q, and caddy's default is eternal: one hung request would hold a retired server open forever", grace)
	}

	vm.peers(t, "curl -sS -m 5 -o /dev/null -H 'Authorization: Bearer TOPSECRET' "+
		"'http://"+caddy.Container+"/callback?code=TOPSECRET&state=xyz'")
	logged := vm.ssh(t, "sudo docker logs --tail 20 "+caddy.Container+" 2>&1 || true")
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
	vm := liveMachine(t)
	vm.purges(t)
	p := vm.provider(t)
	defer closing(t, p)

	ctx := context.Background()
	class := edge.ClassProduction
	bootstrap, err := p.Bootstrap("")
	if err != nil {
		t.Fatal(err)
	}
	if err := bootstrap.Apply(ctx, provider.BootstrapRequest{Class: class, WrittenBy: "live-suite"}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	vm.runs(t, workload)
	defer vm.ssh(t, "sudo docker rm -f "+workload+" >/dev/null 2>&1 || true")

	removal, err := bootstrap.PlanRemove(ctx, class)
	if err != nil {
		t.Fatalf("PlanRemove() = %v", err)
	}
	leaving := onlyGroup(t, removal)
	for _, taken := range []string{caddy.Container, host.SwitchboardContainer, host.ProxyData, host.ProxyNetwork} {
		planned := planFor(leaving, taken)
		if planned.Action != provider.ActionDelete {
			t.Errorf("PlanRemove() plans %s as %q, want it taken: what ocel wrote is what ocel takes back", taken, planned.Action)
		}
		if planned.Reason == "" {
			t.Errorf("PlanRemove() takes %s with no reason, and the typed confirmation must name what goes before a user types", taken)
		}
	}

	if err := bootstrap.Remove(ctx, class, nil); err != nil {
		t.Fatalf("Remove() = %v", err)
	}
	for _, named := range []string{caddy.Container, host.SwitchboardContainer} {
		if vm.running(t, named) {
			t.Errorf("%s still runs after a destroy, and a container nobody wrote back is one nobody takes down", named)
		}
	}
	if stood := vm.inspects(t, "network", host.ProxyNetwork, "{{.Name}}"); stood != "" {
		t.Errorf("the network %s stands after a destroy", host.ProxyNetwork)
	}
	for _, gone := range []string{host.SwitchboardBinary, host.ProxyConfig, host.ProxyData, vars.RoutingTable} {
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
