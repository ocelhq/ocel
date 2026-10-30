package host

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/provider/docker"
	"github.com/ocelhq/ocel/platform/vps/provider/front"
	"github.com/ocelhq/ocel/platform/vps/provider/machine"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/manual"
	"github.com/ocelhq/ocel/platform/vps/provider/routing"
	"github.com/ocelhq/ocel/platform/vps/provider/shell"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

func routedByHand() front.Proxy {
	return front.Proxy{Manual: &front.Manual{Port: manual.DefaultPort}}
}

func coolifysTraefik() front.Proxy {
	return front.Proxy{Traefik: &front.Traefik{
		Preset: "coolify", Directory: "/data/coolify/proxy/dynamic", ContainerDirectory: "/traefik/dynamic", Resolver: "letsencrypt",
		Entrypoints: &front.Entrypoints{HTTP: "http", HTTPS: "https"}, Network: "coolify",
	}}
}

func TestABoxFrontedByHandInstallsNothingOfOcelsOwnProxy(t *testing.T) {
	t.Parallel()

	items := Items(testAssets, environment.TierProduction, []byte(aKey+"\n"), ssh.AMD64, routedByHand())
	for _, item := range items {
		switch {
		case item.Kind == machine.KindContainer && item.Name == caddy.Container,
			item.Kind == machine.KindProxyConfig,
			item.Name == ProxyData, item.Name == boxapi.PinsDir, item.Name == proxyRoot:
			t.Errorf("a box fronted by your own proxy installs %s, which only ocel's own proxy reads", item.ID())
		}
	}
	for _, wanted := range []string{boxapi.SwitchboardBinary, boxapi.RoutingTablePath, ProxyNetwork, boxapi.Switchboard} {
		if !slices.ContainsFunc(items, func(item machine.Item) bool { return item.Name == wanted }) {
			t.Errorf("a box fronted by your own proxy installs no %s; the switchboard still routes it", wanted)
		}
	}
}

func switchboardOf(t *testing.T, fronting front.Proxy) docker.Service {
	t.Helper()
	itemAt(t, ProxyItems(testAssets, ssh.AMD64, fronting), machine.KindContainer, boxapi.Switchboard)
	return switchboardBox(switchboardBinary(testAssets, ssh.AMD64), fronting)
}

func TestTheSwitchboardPublishesOnLoopbackOnlyForAProxyRoutedByHand(t *testing.T) {
	t.Parallel()

	byHand := strings.Join(switchboardOf(t, front.Proxy{Manual: &front.Manual{Port: 9000}}).Run(), " ")
	if !strings.Contains(byHand, "--publish 127.0.0.1:9000:"+routing.SwitchboardPort) {
		t.Errorf("the switchboard runs as %q, want it published on 127.0.0.1:9000 for your proxy to reach", byHand)
	}
	builtIn := strings.Join(switchboardOf(t, front.Proxy{}).Run(), " ")
	if strings.Contains(builtIn, "--publish") {
		t.Errorf("the switchboard runs as %q beside ocel's own proxy, want it reached over the %s network alone", builtIn, ProxyNetwork)
	}
}

func TestAProxyRoutedByHandIsTrustedForItsSchemeAndHostButNeverTheClientItNames(t *testing.T) {
	t.Parallel()

	byHand := switchboardOf(t, routedByHand()).Command
	if at := slices.Index(byHand, "--relay-network"); at < 0 || byHand[at+1] != ProxyNetwork {
		t.Errorf("the switchboard serves as %q beside your proxy, want it relaying from the %s network alone, never an app network it joins, and trusting nobody's X-Forwarded-For", byHand, ProxyNetwork)
	}
	builtIn := switchboardOf(t, front.Proxy{}).Command
	if at := slices.Index(builtIn, "--front"); at < 0 || builtIn[at+1] != boxapi.FrontSocket || slices.Contains(builtIn, "--relay") {
		t.Errorf("the switchboard serves as %q beside ocel's own proxy, want it trusting %s alone, over %s", builtIn, caddy.Container, boxapi.FrontSocket)
	}
}

func routedOnANetwork() front.Proxy {
	return front.Proxy{Manual: &front.Manual{Port: manual.DefaultPort, Network: "coolify"}}
}

func TestAProxyRoutedByHandOnItsOwnNetworkIsHeardFromItAsFromTheBoxNetwork(t *testing.T) {
	t.Parallel()

	board := switchboardOf(t, routedOnANetwork())
	var relayed []string
	for at, arg := range board.Command {
		if arg == "--relay-network" {
			relayed = append(relayed, board.Command[at+1])
		}
	}
	if !slices.Equal(relayed, []string{ProxyNetwork, "coolify"}) {
		t.Errorf("the switchboard serves as %q, want it relaying from %s and from coolify, the network your proxy reaches it on", board.Command, ProxyNetwork)
	}
	if !strings.Contains(shell.Words(board.Run()), shell.Words([]string{"--network", ProxyNetwork, "--network", "coolify"})) {
		t.Errorf("the switchboard runs as %s, want it started on coolify as well as %s: it resolves the networks it relays from as it starts serving", shell.Words(board.Run()), ProxyNetwork)
	}
	if !strings.Contains(shell.Words(board.Run()), shell.Words([]string{"--publish", "127.0.0.1:8480:" + routing.SwitchboardPort})) {
		t.Errorf("the switchboard runs as %s, want it still published on the loopback port beside the network", shell.Words(board.Run()))
	}
}

func TestTheSwitchboardIsWrittenOntoYourProxysNetworkOnlyWhenItExists(t *testing.T) {
	t.Parallel()

	written := switchboardOf(t, routedOnANetwork()).Writing(docker.ContainerRising)
	asked := strings.Index(written, "docker network inspect "+shell.Quote("coolify"))
	ran := strings.Index(written, shell.Quote("docker")+" "+shell.Quote("create"))
	if asked < 0 || ran < 0 || asked > ran {
		t.Fatalf("the switchboard write asks after coolify at %d and runs at %d, want the network found before a run that would fail on it:\n%s", asked, ran, written)
	}
	if !strings.Contains(written, "proxy.manual.network") {
		t.Errorf("the switchboard write refuses a missing network without naming the option that set it:\n%s", written)
	}
}

func TestASwitchboardThatLeftYourProxysNetworkReadsAsDrift(t *testing.T) {
	t.Parallel()

	board := switchboardOf(t, routedOnANetwork())
	if !strings.Contains(containerProbe(board), `index .NetworkSettings.Networks "coolify"`) {
		t.Errorf("the switchboard's probe never asks whether it sits on coolify, so one taken off it is never put back:\n%s", containerProbe(board))
	}
	joined := string(board.Facts())
	left := string(switchboardOf(t, routedByHand()).Facts())
	if joined == left || !strings.Contains(joined, "coolify") {
		t.Errorf("the switchboard is stated as\n%s\non a network and as\n%s\noff it, want its membership of coolify stated", joined, left)
	}
}

func TestAClaimOnABoxFrontedByHandWritesTheTableAloneAndReloadsNothing(t *testing.T) {
	t.Parallel()

	box := &claimBench{bench: newBench(nil), recorded: string(mustWrite(t, routed()))}
	absent := ""
	box.answer = servesPair(box.bench, &box.recorded, &absent)
	h := box.fronted(routedByHand())
	if err := h.routing.ClaimHosts(context.Background(), []boxapi.Claim{{Hostname: claimed, Owner: surface, Pointer: pointed}}); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	table, err := routing.ReadRoutingTable([]byte(box.recorded))
	if err != nil {
		t.Fatal(err)
	}
	if len(table.Claims) != 1 || table.Claims[0].Hostname != claimed {
		t.Errorf("%s records claims %v, want %s claimed", boxapi.RoutingTablePath, table.Claims, claimed)
	}
	if absent != "" {
		t.Errorf("the claim wrote %s as %q, want nothing written for a proxy ocel never writes to", ProxyConfig, absent)
	}
	for _, command := range box.commands() {
		if writesProxy(command) && strings.Contains(command, ProxyConfig) {
			t.Errorf("the write asks for %s, which a box fronted by your own proxy never has: %s", ProxyConfig, command)
		}
	}
	if !slices.ContainsFunc(box.commands(), loadsSwitchboard) {
		t.Errorf("the claim was never loaded onto the switchboard: %v", box.commands())
	}
	if slices.ContainsFunc(box.commands(), reloadsFront) {
		t.Errorf("the claim reloaded %s on a box whose proxy is yours: %v", caddy.Container, box.commands())
	}
}

func TestTheConnectorOnABoxYourProxyFrontsSaysWhatToRouteToIt(t *testing.T) {
	t.Parallel()

	box := &claimBench{bench: newBench(nil), recorded: string(mustWrite(t, routed()))}
	absent := ""
	box.answer = servesPair(box.bench, &box.recorded, &absent)
	progress := &said{}
	if _, err := NewConnector(box.fronted(routedByHand())).Install(context.Background(), "box.example.com", []byte("a connector"), connectorConfig(), progress); err != nil {
		t.Fatalf("Install() = %v", err)
	}
	if want := manual.Route("box.example.com", manual.DefaultPort); progress.at(want) < 0 {
		t.Errorf("the install said %q, want %q: the console reaches the connector through your proxy", progress.lines, want)
	}
}

func TestAProxyRoutedByHandOnItsOwnNetworkIsToldTheSwitchboardsNameOnIt(t *testing.T) {
	t.Parallel()

	got := New(nil, testAssets, "shop", Keys{}, nil, routedOnANetwork()).RouteBy("shop.example.com")
	want := "Route shop.example.com → http://ocel-switchboard:8080 on the coolify network, or http://127.0.0.1:8480 from the host (keep Host, set X-Forwarded-Proto)"
	if got != want {
		t.Errorf("RouteBy() = %q, want %q: a proxy in a container on coolify cannot reach the host's loopback", got, want)
	}
}

func frontRecordItem(fronting front.Proxy, project string, tier environment.Tier) (machine.Item, error) {
	return frontRecord{Proxy: fronting.Recorded(), Project: project, Tier: tier}.item()
}

func recordOn(t *testing.T, box *bench, tier environment.Tier, fronting front.Proxy, project string) {
	t.Helper()
	record, err := frontRecordItem(fronting, project, tier)
	if err != nil {
		t.Fatal(err)
	}
	box.installed[tier] = append(unrecorded(box, tier), record)
}

func unrecorded(box *bench, tier environment.Tier) []machine.Item {
	return slices.DeleteFunc(box.installed[tier], func(item machine.Item) bool { return item.Name == FrontRecordPath })
}

func TestABootstrapRecordsWhichProxyFrontsTheBoxAndWhoSetIt(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := bootstrappedOn(t, tier)
	box.installed[tier] = unrecorded(box, tier)
	if err := NewBootstrap(box.host(), testVendor, "shop").Apply(context.Background(),
		provider.BootstrapRequest{Tier: tier, WrittenBy: "the-suite"}, progress.Discard()); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	at := box.at("/dev/stdin " + shell.Quote(FrontRecordPath))
	if at < 0 {
		t.Fatalf("the apply never wrote %s:\n%s", FrontRecordPath, strings.Join(box.commands(), "\n"))
	}
	written := box.fed[at]
	for _, wanted := range []string{`"proxy":null`, `"project":"shop"`, `"tier":"production"`} {
		if !strings.Contains(written, wanted) {
			t.Errorf("%s was written as %s, want %s in it", FrontRecordPath, written, wanted)
		}
	}
}

func TestABootstrapLeavesARecordThatAgreesUnchanged(t *testing.T) {
	t.Parallel()

	tier := environment.TierPreview
	box := bootstrappedOn(t, tier)
	recordOn(t, box, tier, front.Proxy{}, "blog")
	if err := NewBootstrap(box.host(), testVendor, "shop").Apply(context.Background(),
		provider.BootstrapRequest{Tier: tier, WrittenBy: "the-suite"}, progress.Discard()); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if at := box.at("/dev/stdin " + shell.Quote(FrontRecordPath)); at >= 0 {
		t.Errorf("the apply rewrote %s, which blog set and this project agrees with: %s", FrontRecordPath, box.commands()[at])
	}
}

func TestADeployOntoABoxRecordedForAnotherProxyIsRefusedNamingWhoSetIt(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		recorded front.Proxy
		ours     front.Proxy
		wanted   []string
	}{
		"the box routes by hand, the project leaves the option out": {
			recorded: routedByHand(), ours: front.Proxy{},
			wanted: []string{"a proxy you route yourself", "set by blog/preview", "add `\"proxy\": \"manual\"`"},
		},
		"the box routes by hand on another port": {
			recorded: front.Proxy{Manual: &front.Manual{Port: 9000}}, ours: routedByHand(),
			wanted: []string{"set by blog/preview", "add `\"proxy\": { \"manual\": { \"port\": 9000 } }`"},
		},
		"the box routes by hand beside a network": {
			recorded: front.Proxy{Manual: &front.Manual{Port: manual.DefaultPort, Network: "coolify"}}, ours: routedByHand(),
			wanted: []string{"a proxy you route yourself", "add `\"proxy\": { \"manual\": { \"network\": \"coolify\" } }`"},
		},
		"the box runs Coolify's Traefik": {
			recorded: coolifysTraefik(), ours: routedByHand(),
			wanted: []string{"Coolify's Traefik", "set by blog/preview", "add `\"proxy\": { \"traefik\": { \"preset\": \"coolify\" } }`"},
		},
		"the box runs Coolify's Traefik with a resolver of its own": {
			recorded: func() front.Proxy {
				front := coolifysTraefik()
				front.Traefik.Resolver = "le-dns"
				return front
			}(),
			ours:   coolifysTraefik(),
			wanted: []string{"Coolify's Traefik", "add `\"proxy\": { \"traefik\": { \"preset\": \"coolify\", \"resolver\": \"le-dns\" } }`"},
		},
		"the box runs Coolify's Traefik reached on a port": {
			recorded: onAPort(coolifysTraefik()),
			ours:     coolifysTraefik(),
			wanted:   []string{"Coolify's Traefik", "add `\"proxy\": { \"traefik\": { \"preset\": \"coolify\", \"port\": 9000 } }`"},
		},
		"the box runs a Traefik spelled out": {
			recorded: front.Proxy{Traefik: &front.Traefik{
				Directory: "/etc/traefik/dynamic", Resolver: "letsencrypt", PreviewResolver: "cloudflare",
				Entrypoints: &front.Entrypoints{HTTP: "web", HTTPS: "websecure"}, Network: "traefik",
			}},
			ours: front.Proxy{},
			wanted: []string{"your Traefik", "add `\"proxy\": { \"traefik\": { \"directory\": \"/etc/traefik/dynamic\", \"resolver\": \"letsencrypt\", " +
				"\"previewResolver\": \"cloudflare\", \"network\": \"traefik\" } }`"},
		},
		"the box runs a Traefik on its own port and entry points": {
			recorded: front.Proxy{Traefik: &front.Traefik{
				Directory: "/etc/traefik/dynamic", Resolver: "letsencrypt",
				Entrypoints: &front.Entrypoints{HTTP: "insecure", HTTPS: "secure"}, Port: 9000,
			}},
			ours: front.Proxy{},
			wanted: []string{"add `\"proxy\": { \"traefik\": { \"directory\": \"/etc/traefik/dynamic\", \"resolver\": \"letsencrypt\", " +
				"\"entrypoints\": { \"http\": \"insecure\", \"https\": \"secure\" }, \"port\": 9000 } }`"},
		},
		"the box runs Coolify's Caddy": {
			recorded: front.Proxy{Caddy: &front.Caddy{
				Preset: "coolify", Directory: "/data/coolify/proxy/caddy/dynamic", ContainerDirectory: "/dynamic", Container: "coolify-proxy",
				Config: "/config/caddy/Caddyfile.autosave", Network: "coolify",
			}},
			ours:   coolifysTraefik(),
			wanted: []string{"Coolify's Caddy", "add `\"proxy\": { \"caddy\": { \"preset\": \"coolify\" } }`"},
		},
		"the box runs a systemd Caddy": {
			recorded: front.Proxy{Caddy: &front.Caddy{Directory: "/etc/caddy/ocel.d", Config: "/etc/caddy/Caddyfile", Port: 8480}},
			ours:     routedByHand(),
			wanted:   []string{"your Caddy", "add `\"proxy\": { \"caddy\": { \"directory\": \"/etc/caddy/ocel.d\" } }`"},
		},
		"the box runs ocel's own proxy, the project routes by hand": {
			recorded: front.Proxy{}, ours: routedByHand(),
			wanted: []string{"ocel's own proxy", "set by blog/preview", "remove `\"proxy\"`"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			box := bootstrappedOn(t, environment.TierPreview)
			recordOn(t, box, environment.TierPreview, tc.recorded, "blog")
			refused := refusalOf(t, box.fronted(tc.ours).RefuseDisagreeingFront(context.Background(), environment.TierPreview), refusal.CodeInvalid)
			for _, wanted := range tc.wanted {
				if !strings.Contains(refused.Message, wanted) {
					t.Errorf("the refusal says %q, want %q in it", refused.Message, wanted)
				}
			}
		})
	}
}

func TestADeployOntoABoxRecordedForItsOwnProxyGoesAhead(t *testing.T) {
	t.Parallel()

	box := bootstrappedOn(t, environment.TierProduction)
	recordOn(t, box, environment.TierProduction, routedByHand(), "blog")
	if err := box.fronted(routedByHand()).RefuseDisagreeingFront(context.Background(), environment.TierProduction); err != nil {
		t.Errorf("RefuseDisagreeingFront() = %v, want a box that routes the way this project says let through", err)
	}
}

func TestAPresetAndTheSameProxySpelledOutAreOneProxy(t *testing.T) {
	t.Parallel()

	spelled := front.Proxy{Traefik: &front.Traefik{
		Directory: "/data/coolify/proxy/dynamic", ContainerDirectory: "/traefik/dynamic", Resolver: "letsencrypt",
		Entrypoints: &front.Entrypoints{HTTP: "http", HTTPS: "https"}, Network: "coolify",
	}}
	for name, tc := range map[string]struct{ recorded, ours front.Proxy }{
		"recorded as the preset, deployed spelled out": {recorded: coolifysTraefik(), ours: spelled},
		"recorded spelled out, deployed as the preset": {recorded: spelled, ours: coolifysTraefik()},
		"recorded as the preset on a port, deployed spelled out on it": {
			recorded: onAPort(coolifysTraefik()),
			ours:     onAPort(front.Proxy{Traefik: spelled.Traefik}),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			box := bootstrappedOn(t, environment.TierProduction)
			recordOn(t, box, environment.TierProduction, tc.recorded, "blog")
			if err := box.fronted(tc.ours).RefuseDisagreeingFront(context.Background(), environment.TierProduction); err != nil {
				t.Errorf("RefuseDisagreeingFront() = %v, want a preset and the values it fills agreed as one proxy", err)
			}
		})
	}
}

func onAPort(fronting front.Proxy) front.Proxy {
	moved := *fronting.Traefik
	moved.Network, moved.Port = "", 9000
	return front.Proxy{Traefik: &moved}
}

func TestADeployOntoABoxThatRecordsNoProxyIsSentToBootstrap(t *testing.T) {
	t.Parallel()

	box := bootstrappedOn(t, environment.TierProduction)
	box.installed[environment.TierProduction] = unrecorded(box, environment.TierProduction)
	refused := refusalOf(t, box.host().RefuseDisagreeingFront(context.Background(), environment.TierProduction), refusal.CodeNotReady)
	if !strings.Contains(refused.Message, FrontRecordPath) || !strings.Contains(refused.Message, "ocel bootstrap") {
		t.Errorf("the refusal says %q, want it to name %s and the bootstrap that writes it", refused.Message, FrontRecordPath)
	}
}

func TestTheLastTierToGoTakesTheRecordWithIt(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := newBench(map[environment.Tier][]machine.Item{tier: bootstrapped(t, tier)})
	recordOn(t, box, tier, front.Proxy{}, "shop")
	plan, err := NewBootstrap(box.host(), testVendor, "shop").PlanRemove(context.Background(), tier)
	if err != nil {
		t.Fatalf("PlanRemove() = %v", err)
	}
	if !slices.ContainsFunc(plan.Groups[0].Changes, func(change provider.Change) bool {
		return change.Name == FrontRecordPath && change.Action == provider.ActionDelete
	}) {
		t.Errorf("the removal plan %+v leaves %s behind, and the next bootstrap would read it as another project's", plan.Groups[0].Changes, FrontRecordPath)
	}
}

func recordChange(t *testing.T, plan provider.Plan) provider.Change {
	t.Helper()
	for _, change := range plan.Groups[0].Changes {
		if change.Name == FrontRecordPath {
			return change
		}
	}
	t.Fatalf("the plan %+v says nothing of %s", plan.Groups[0].Changes, FrontRecordPath)
	return provider.Change{}
}

func TestABootstrapUnderAProxyRoutedByHandOntoABoxOcelsOwnProxyFrontsUnrecordedMovesItOffOcelsOwnProxy(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := bootstrappedOn(t, tier)
	box.installed[tier] = unrecorded(box, tier)
	move := movePlanned(t, box, tier, routedByHand())
	if want := string(testVendor) + "/ocel's own proxy → a proxy you route yourself"; move.Name != want {
		t.Errorf("the plan names the move %q, want %q: %s runs on this box, so it is fronted by ocel's own proxy whether or not a record says so", move.Name, want, caddy.Container)
	}
	survey := box.commands()[box.at("for p in")]
	if !strings.Contains(survey, "docker inspect --type container") || !strings.Contains(survey, shell.Quote(caddy.Container)) {
		t.Errorf("the survey under a proxy routed by hand never asks after %s, so nothing it reads can say ocel's own proxy fronts the box:\n%s", caddy.Container, survey)
	}
}

func TestABootstrapOverADeletedRecordPlansItAsDriftAndWritesItBack(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	box := bootstrappedOn(t, tier)
	box.installed[tier] = unrecorded(box, tier)
	boot := NewBootstrap(box.host(), testVendor, "shop")
	described, err := boot.Describe(context.Background(), tier)
	if err != nil {
		t.Fatalf("Describe() = %v", err)
	}
	if described.Stacks[0].DigestCurrent {
		t.Error("Describe() calls a box whose record was deleted current, so no bootstrap would ever write it back and every deploy refuses")
	}
	plan, err := boot.Plan(context.Background(), provider.BootstrapRequest{Tier: tier})
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if plan.Groups[0].Action != provider.ActionUpdate {
		t.Errorf("the plan acts %q on a box missing its record, want %q", plan.Groups[0].Action, provider.ActionUpdate)
	}
	if change := recordChange(t, plan); change.Action != provider.ActionCreate {
		t.Errorf("the plan %s %s, want it created", change.Action, FrontRecordPath)
	}
}

func TestABootstrapOfAFreshBoxUnderAProxyRoutedByHandPlansItsRecord(t *testing.T) {
	t.Parallel()

	fresh := newBench(nil)
	fresh.answer = func(command string) (ssh.Result, bool) {
		return ssh.Result{Stdout: aKey + "\n"}, command == "cat ~/.ssh/authorized_keys 2>/dev/null"
	}
	plan, err := NewBootstrap(fresh.fronted(routedByHand()), testVendor, "shop").Plan(context.Background(),
		provider.BootstrapRequest{Tier: environment.TierProduction})
	if err != nil {
		t.Fatalf("Plan() = %v, want a fresh box, where nothing of ocel's is installed, free to take a proxy routed by hand", err)
	}
	if change := recordChange(t, plan); change.Action != provider.ActionCreate {
		t.Errorf("the plan %s %s on a fresh box, want it created", change.Action, FrontRecordPath)
	}
}

func TestWhatABoxInstallsFollowsWhetherItsProxyOwnsThePorts(t *testing.T) {
	t.Parallel()

	for name, front := range map[string]front.Proxy{"ocel's own proxy": {}, "a proxy routed by hand": routedByHand(), "a Traefik on the host": traefikOnTheHost()} {
		owns := front.Front().Open(frontBox{}).Guarantees().OwnsPorts
		installsProxy := slices.ContainsFunc(ProxyItems(testAssets, ssh.AMD64, front), func(item machine.Item) bool {
			return item.Kind == machine.KindContainer && item.Name == caddy.Container
		})
		if installsProxy != owns {
			t.Errorf("%s: the box installs %s = %v, want %v: only a proxy that owns 80 and 443 is one ocel runs", name, caddy.Container, installsProxy, owns)
		}
		if published := len(switchboardOf(t, front).Ports) > 0; published == owns {
			t.Errorf("%s: the switchboard publishes a port = %v, want it published only for a proxy that does not own the ports", name, published)
		}
		if recorded := front.Recorded() != nil; recorded == owns {
			t.Errorf("%s: the record names a proxy = %v, want one named only for a proxy that does not own the ports", name, recorded)
		}
	}
}
