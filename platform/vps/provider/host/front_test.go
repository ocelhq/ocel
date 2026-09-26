package host

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/manual"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func routedByHand() Front { return Front{Manual: &ManualFront{Port: manual.DefaultPort}} }

func coolifysTraefik() Front {
	return Front{Traefik: &TraefikFront{
		Preset: "coolify", Directory: "/data/coolify/proxy/dynamic", Resolver: "letsencrypt",
		Entrypoints: Entrypoints{HTTP: "http", HTTPS: "https"}, Network: "coolify",
	}}
}

func TestABoxFrontedByHandStandsNothingOfOcelsOwnProxy(t *testing.T) {
	t.Parallel()

	items := Items(edge.ClassProduction, []byte(aKey+"\n"), ArchAMD64, routedByHand())
	for _, item := range items {
		switch {
		case item.Kind == KindContainer && item.Name == caddy.Container,
			item.Kind == KindProxyConfig,
			item.Name == ProxyData, item.Name == caddy.PinsDir, item.Name == proxyRoot:
			t.Errorf("a box fronted by your own proxy stands %s, which only ocel's own proxy reads", item.ID())
		}
	}
	for _, wanted := range []string{SwitchboardBinary, live.RoutingTable, ProxyNetwork, SwitchboardContainer} {
		if !slices.ContainsFunc(items, func(item Item) bool { return item.Name == wanted }) {
			t.Errorf("a box fronted by your own proxy stands no %s; the switchboard still routes it", wanted)
		}
	}
}

func switchboardOf(t *testing.T, front Front) boxContainer {
	t.Helper()
	board := itemAt(t, ProxyItems(ArchAMD64, front), KindContainer, SwitchboardContainer)
	return *board.box
}

func TestTheSwitchboardPublishesOnLoopbackOnlyForAProxyRoutedByHand(t *testing.T) {
	t.Parallel()

	byHand := strings.Join(switchboardOf(t, Front{Manual: &ManualFront{Port: 9000}}).run(), " ")
	if !strings.Contains(byHand, "--publish 127.0.0.1:9000:"+switchboardPort) {
		t.Errorf("the switchboard runs as %q, want it published on 127.0.0.1:9000 for your proxy to reach", byHand)
	}
	builtIn := strings.Join(switchboardOf(t, Front{}).run(), " ")
	if strings.Contains(builtIn, "--publish") {
		t.Errorf("the switchboard runs as %q beside ocel's own proxy, want it reached over the %s network alone", builtIn, ProxyNetwork)
	}
}

func TestAProxyRoutedByHandIsTrustedForItsSchemeAndHostButNeverTheClientItNames(t *testing.T) {
	t.Parallel()

	byHand := switchboardOf(t, routedByHand()).command
	if at := slices.Index(byHand, "--relay-network"); at < 0 || byHand[at+1] != ProxyNetwork {
		t.Errorf("the switchboard serves as %q beside your proxy, want it relaying from the %s network alone, never an app network it joins, and trusting nobody's X-Forwarded-For", byHand, ProxyNetwork)
	}
	builtIn := switchboardOf(t, Front{}).command
	if at := slices.Index(builtIn, "--front"); at < 0 || builtIn[at+1] != switchboard.FrontSocket || slices.Contains(builtIn, "--relay") {
		t.Errorf("the switchboard serves as %q beside ocel's own proxy, want it trusting %s alone, over %s", builtIn, caddy.Container, switchboard.FrontSocket)
	}
}

func routedOnANetwork() Front {
	return Front{Manual: &ManualFront{Port: manual.DefaultPort, Network: "coolify"}}
}

func TestAProxyRoutedByHandOnItsOwnNetworkIsHeardFromItAsFromTheBoxNetwork(t *testing.T) {
	t.Parallel()

	board := switchboardOf(t, routedOnANetwork())
	var relayed []string
	for at, arg := range board.command {
		if arg == "--relay-network" {
			relayed = append(relayed, board.command[at+1])
		}
	}
	if !slices.Equal(relayed, []string{ProxyNetwork, "coolify"}) {
		t.Errorf("the switchboard serves as %q, want it relaying from %s and from coolify, the network your proxy reaches it on", board.command, ProxyNetwork)
	}
	if !strings.Contains(words(board.run()), words([]string{"--network", ProxyNetwork, "--network", "coolify"})) {
		t.Errorf("the switchboard runs as %s, want it started on coolify as well as %s: it resolves the networks it relays from as it starts serving", words(board.run()), ProxyNetwork)
	}
	if !strings.Contains(words(board.run()), words([]string{"--publish", "127.0.0.1:8480:" + switchboardPort})) {
		t.Errorf("the switchboard runs as %s, want it still published on the loopback port beside the network", words(board.run()))
	}
}

func TestTheSwitchboardIsWrittenOntoYourProxysNetworkOnlyWhenItStands(t *testing.T) {
	t.Parallel()

	written := switchboardOf(t, routedOnANetwork()).writing(containerRising)
	asked := strings.Index(written, "docker network inspect "+quoted("coolify"))
	ran := strings.Index(written, quoted("run")+" "+quoted("--detach"))
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
	if !strings.Contains(board.probe(), `index .NetworkSettings.Networks "coolify"`) {
		t.Errorf("the switchboard's probe never asks whether it sits on coolify, so one taken off it is never put back:\n%s", board.probe())
	}
	joined := string(board.facts())
	left := string(switchboardOf(t, routedByHand()).facts())
	if joined == left || !strings.Contains(joined, "coolify") {
		t.Errorf("the switchboard is stated as\n%s\non a network and as\n%s\noff it, want its membership of coolify stated", joined, left)
	}
}

func TestAClaimOnABoxFrontedByHandWritesTheTableAloneAndReloadsNothing(t *testing.T) {
	t.Parallel()

	stood := &claimBench{bench: machine(nil), held: string(mustWrite(t, routed()))}
	absent := ""
	stood.answer = servesPair(stood.bench, &stood.held, &absent)
	h := stood.fronted(routedByHand())
	if err := h.ClaimHosts(context.Background(), []HostClaim{{Hostname: claimed, Owner: surface, Pointer: pointed}}); err != nil {
		t.Fatalf("ClaimHosts() = %v", err)
	}
	held, err := ReadRoutingTable([]byte(stood.held))
	if err != nil {
		t.Fatal(err)
	}
	if len(held.Claims) != 1 || held.Claims[0].Hostname != claimed {
		t.Errorf("%s holds claims %v, want %s claimed", live.RoutingTable, held.Claims, claimed)
	}
	if absent != "" {
		t.Errorf("the claim wrote %s as %q, want nothing written for a proxy ocel never writes to", ProxyConfig, absent)
	}
	for _, command := range stood.commands() {
		if writesProxy(command) && strings.Contains(command, ProxyConfig) {
			t.Errorf("the write asks for %s, which a box fronted by your own proxy never has: %s", ProxyConfig, command)
		}
	}
	if !slices.ContainsFunc(stood.commands(), loadsSwitchboard) {
		t.Errorf("the claim was never loaded onto the switchboard: %v", stood.commands())
	}
	if slices.ContainsFunc(stood.commands(), reloadsFront) {
		t.Errorf("the claim reloaded %s on a box whose proxy is yours: %v", caddy.Container, stood.commands())
	}
}

func TestTheConnectorOnABoxYourProxyFrontsSaysWhatToRouteToIt(t *testing.T) {
	t.Parallel()

	stood := &claimBench{bench: machine(nil), held: string(mustWrite(t, routed()))}
	absent := ""
	stood.answer = servesPair(stood.bench, &stood.held, &absent)
	progress := &said{}
	if _, err := NewConnector(stood.fronted(routedByHand())).Install(context.Background(), "box.example.com", []byte("a connector"), connectorConfig(), progress); err != nil {
		t.Fatalf("Install() = %v", err)
	}
	if want := manual.Route("box.example.com", manual.DefaultPort); progress.at(want) < 0 {
		t.Errorf("the install said %q, want %q: the console reaches the connector through your proxy", progress.lines, want)
	}
}

func TestAProxyRoutedByHandOnItsOwnNetworkIsToldTheSwitchboardsNameOnIt(t *testing.T) {
	t.Parallel()

	got := New(nil, Keys{}, nil, routedOnANetwork()).RouteBy("shop.example.com")
	want := "Route shop.example.com → http://ocel-switchboard:8080 on the coolify network, or http://127.0.0.1:8480 from the host (keep Host, set X-Forwarded-Proto)"
	if got != want {
		t.Errorf("RouteBy() = %q, want %q: a proxy in a container on coolify cannot reach the host's loopback", got, want)
	}
}

func frontRecordItem(front Front, project string, class edge.Class) (Item, error) {
	return frontRecord{Proxy: front.recorded(), Project: project, Class: class}.item()
}

func recordOn(t *testing.T, stood *bench, class edge.Class, front Front, project string) {
	t.Helper()
	record, err := frontRecordItem(front, project, class)
	if err != nil {
		t.Fatal(err)
	}
	stood.stands[class] = append(unrecorded(stood, class), record)
}

func unrecorded(stood *bench, class edge.Class) []Item {
	return slices.DeleteFunc(stood.stands[class], func(item Item) bool { return item.Name == FrontRecordPath })
}

func TestABootstrapRecordsWhichProxyFrontsTheBoxAndWhoSetIt(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	stood := settledOn(t, class)
	stood.stands[class] = unrecorded(stood, class)
	if err := NewBootstrap(stood.host(), testVendor, "shop").Apply(context.Background(),
		provider.BootstrapRequest{Class: class, WrittenBy: "the-suite"}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	at := stood.at("/dev/stdin " + quoted(FrontRecordPath))
	if at < 0 {
		t.Fatalf("the apply never wrote %s:\n%s", FrontRecordPath, strings.Join(stood.commands(), "\n"))
	}
	written := stood.fed[at]
	for _, wanted := range []string{`"proxy":null`, `"project":"shop"`, `"class":"production"`} {
		if !strings.Contains(written, wanted) {
			t.Errorf("%s was written as %s, want %s in it", FrontRecordPath, written, wanted)
		}
	}
}

func TestABootstrapLeavesARecordThatAgreesAsItStands(t *testing.T) {
	t.Parallel()

	class := edge.ClassPreview
	stood := settledOn(t, class)
	recordOn(t, stood, class, Front{}, "blog")
	if err := NewBootstrap(stood.host(), testVendor, "shop").Apply(context.Background(),
		provider.BootstrapRequest{Class: class, WrittenBy: "the-suite"}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if at := stood.at("/dev/stdin " + quoted(FrontRecordPath)); at >= 0 {
		t.Errorf("the apply rewrote %s, which blog set and this project agrees with: %s", FrontRecordPath, stood.commands()[at])
	}
}

func TestABootstrapWhoseProxyTheBoxDoesNotRouteThroughIsRefusedWithWhatToWrite(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	stood := settledOn(t, class)
	recordOn(t, stood, class, routedByHand(), "blog")
	_, err := NewBootstrap(stood.host(), testVendor, "shop").Plan(context.Background(), provider.BootstrapRequest{Class: class})
	refused := refusalOf(t, err, refusal.CodeInvalid)
	for _, wanted := range []string{"a proxy you route yourself", "set by blog/production", "add `\"proxy\": \"manual\"`"} {
		if !strings.Contains(refused.Message, wanted) {
			t.Errorf("the refusal says %q, want %q in it", refused.Message, wanted)
		}
	}
}

func TestADeployOntoABoxRecordedForAnotherProxyIsRefusedNamingWhoSetIt(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		recorded Front
		ours     Front
		wanted   []string
	}{
		"the box routes by hand, the project leaves the option out": {
			recorded: routedByHand(), ours: Front{},
			wanted: []string{"a proxy you route yourself", "set by blog/preview", "add `\"proxy\": \"manual\"`"},
		},
		"the box routes by hand on another port": {
			recorded: Front{Manual: &ManualFront{Port: 9000}}, ours: routedByHand(),
			wanted: []string{"set by blog/preview", "add `\"proxy\": { \"manual\": { \"port\": 9000 } }`"},
		},
		"the box routes by hand beside a network": {
			recorded: Front{Manual: &ManualFront{Port: manual.DefaultPort, Network: "coolify"}}, ours: routedByHand(),
			wanted: []string{"a proxy you route yourself", "add `\"proxy\": { \"manual\": { \"network\": \"coolify\" } }`"},
		},
		"the box runs Coolify's Traefik": {
			recorded: coolifysTraefik(), ours: routedByHand(),
			wanted: []string{"Coolify's Traefik", "set by blog/preview", "add `\"proxy\": { \"traefik\": { \"preset\": \"coolify\" } }`"},
		},
		"the box runs Coolify's Traefik with a resolver of its own": {
			recorded: func() Front {
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
			recorded: Front{Traefik: &TraefikFront{
				Directory: "/etc/traefik/dynamic", Resolver: "letsencrypt", PreviewResolver: "cloudflare",
				Entrypoints: Entrypoints{HTTP: "web", HTTPS: "websecure"}, Network: "traefik",
			}},
			ours: Front{},
			wanted: []string{"your Traefik", "add `\"proxy\": { \"traefik\": { \"directory\": \"/etc/traefik/dynamic\", \"resolver\": \"letsencrypt\", " +
				"\"previewResolver\": \"cloudflare\", \"network\": \"traefik\" } }`"},
		},
		"the box runs a Traefik on its own port and entry points": {
			recorded: Front{Traefik: &TraefikFront{
				Directory: "/etc/traefik/dynamic", Resolver: "letsencrypt",
				Entrypoints: Entrypoints{HTTP: "insecure", HTTPS: "secure"}, Port: 9000,
			}},
			ours: Front{},
			wanted: []string{"add `\"proxy\": { \"traefik\": { \"directory\": \"/etc/traefik/dynamic\", \"resolver\": \"letsencrypt\", " +
				"\"entrypoints\": { \"http\": \"insecure\", \"https\": \"secure\" }, \"port\": 9000 } }`"},
		},
		"the box runs Coolify's Caddy": {
			recorded: Front{Caddy: &CaddyFront{
				Preset: "coolify", Directory: "/data/coolify/proxy/caddy/dynamic", Container: "coolify-proxy",
				Config: "/config/caddy/Caddyfile.autosave", Network: "coolify",
			}},
			ours:   coolifysTraefik(),
			wanted: []string{"Coolify's Caddy", "add `\"proxy\": { \"caddy\": { \"preset\": \"coolify\" } }`"},
		},
		"the box runs a systemd Caddy": {
			recorded: Front{Caddy: &CaddyFront{Directory: "/etc/caddy/ocel.d", Config: "/etc/caddy/Caddyfile", Port: 8480}},
			ours:     routedByHand(),
			wanted:   []string{"your Caddy", "add `\"proxy\": { \"caddy\": { \"directory\": \"/etc/caddy/ocel.d\" } }`"},
		},
		"the box runs ocel's own proxy, the project routes by hand": {
			recorded: Front{}, ours: routedByHand(),
			wanted: []string{"ocel's own proxy", "set by blog/preview", "remove `\"proxy\"`"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			stood := settledOn(t, edge.ClassPreview)
			recordOn(t, stood, edge.ClassPreview, tc.recorded, "blog")
			refused := refusalOf(t, stood.fronted(tc.ours).FrontAgrees(context.Background()), refusal.CodeInvalid)
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

	stood := settledOn(t, edge.ClassProduction)
	recordOn(t, stood, edge.ClassProduction, routedByHand(), "blog")
	if err := stood.fronted(routedByHand()).FrontAgrees(context.Background()); err != nil {
		t.Errorf("FrontAgrees() = %v, want a box that routes the way this project says let through", err)
	}
}

func TestAPresetAndTheSameProxySpelledOutAreOneProxy(t *testing.T) {
	t.Parallel()

	spelled := Front{Traefik: &TraefikFront{
		Directory: "/data/coolify/proxy/dynamic", Resolver: "letsencrypt",
		Entrypoints: Entrypoints{HTTP: "http", HTTPS: "https"}, Network: "coolify",
	}}
	for name, tc := range map[string]struct{ recorded, ours Front }{
		"recorded as the preset, deployed spelled out": {recorded: coolifysTraefik(), ours: spelled},
		"recorded spelled out, deployed as the preset": {recorded: spelled, ours: coolifysTraefik()},
		"recorded as the preset on a port, deployed spelled out on it": {
			recorded: onAPort(coolifysTraefik()),
			ours:     onAPort(Front{Traefik: spelled.Traefik}),
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			stood := settledOn(t, edge.ClassProduction)
			recordOn(t, stood, edge.ClassProduction, tc.recorded, "blog")
			if err := stood.fronted(tc.ours).FrontAgrees(context.Background()); err != nil {
				t.Errorf("FrontAgrees() = %v, want a preset and the values it fills agreed as one proxy", err)
			}
		})
	}
}

func onAPort(front Front) Front {
	moved := *front.Traefik
	moved.Network, moved.Port = "", 9000
	return Front{Traefik: &moved}
}

func TestADeployOntoABoxThatRecordsNoProxyIsSentToBootstrap(t *testing.T) {
	t.Parallel()

	stood := settledOn(t, edge.ClassProduction)
	stood.stands[edge.ClassProduction] = unrecorded(stood, edge.ClassProduction)
	refused := refusalOf(t, stood.host().FrontAgrees(context.Background()), refusal.CodeNotReady)
	if !strings.Contains(refused.Message, FrontRecordPath) || !strings.Contains(refused.Message, "ocel bootstrap") {
		t.Errorf("the refusal says %q, want it to name %s and the bootstrap that writes it", refused.Message, FrontRecordPath)
	}
}

func TestTheLastClassToGoTakesTheRecordWithIt(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	stood := machine(map[edge.Class][]Item{class: bootstrapped(t, class)})
	recordOn(t, stood, class, Front{}, "shop")
	plan, err := NewBootstrap(stood.host(), testVendor, "shop").PlanRemove(context.Background(), class)
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

func TestABootstrapUnderAProxyRoutedByHandOntoABoxOcelsOwnProxyFrontsUnrecordedIsRefused(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	stood := settledOn(t, class)
	stood.stands[class] = unrecorded(stood, class)
	boot := NewBootstrap(stood.fronted(routedByHand()), testVendor, "shop")
	_, planned := boot.Plan(context.Background(), provider.BootstrapRequest{Class: class})
	applied := boot.Apply(context.Background(), provider.BootstrapRequest{Class: class, WrittenBy: "the-suite"}, nil)
	for step, err := range map[string]error{"Plan": planned, "Apply": applied} {
		refused := refusalOf(t, err, refusal.CodeInvalid)
		for _, wanted := range []string{"ocel's own proxy", "remove `\"proxy\"`"} {
			if !strings.Contains(refused.Message, wanted) {
				t.Errorf("%s refused with %q, want %q in it: %s stands on this box, so it is fronted by ocel's own proxy whether or not a record says so", step, refused.Message, wanted, caddy.Container)
			}
		}
	}
	if at := stood.at("/dev/stdin " + quoted(FrontRecordPath)); at >= 0 {
		t.Errorf("the refused bootstrap still wrote %s: %s", FrontRecordPath, stood.commands()[at])
	}
	survey := stood.commands()[stood.at("for p in")]
	if !strings.Contains(survey, "docker inspect --type container") || !strings.Contains(survey, quoted(caddy.Container)) {
		t.Errorf("the survey under a proxy routed by hand never asks after %s, so nothing it reads can say ocel's own proxy fronts the box:\n%s", caddy.Container, survey)
	}
}

func TestABootstrapOverADeletedRecordPlansItAsDriftAndWritesItBack(t *testing.T) {
	t.Parallel()

	class := edge.ClassProduction
	stood := settledOn(t, class)
	stood.stands[class] = unrecorded(stood, class)
	boot := NewBootstrap(stood.host(), testVendor, "shop")
	described, err := boot.Describe(context.Background(), class)
	if err != nil {
		t.Fatalf("Describe() = %v", err)
	}
	if described.Stacks[0].DigestCurrent {
		t.Error("Describe() calls a box whose record was deleted current, so no bootstrap would ever write it back and every deploy refuses")
	}
	plan, err := boot.Plan(context.Background(), provider.BootstrapRequest{Class: class})
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

	fresh := machine(nil)
	fresh.answer = func(command string) (session.Result, bool) {
		return session.Result{Stdout: aKey + "\n"}, command == "cat ~/.ssh/authorized_keys 2>/dev/null"
	}
	plan, err := NewBootstrap(fresh.fronted(routedByHand()), testVendor, "shop").Plan(context.Background(),
		provider.BootstrapRequest{Class: edge.ClassProduction})
	if err != nil {
		t.Fatalf("Plan() = %v, want a fresh box, where nothing of ocel's stands, free to take a proxy routed by hand", err)
	}
	if change := recordChange(t, plan); change.Action != provider.ActionCreate {
		t.Errorf("the plan %s %s on a fresh box, want it created", change.Action, FrontRecordPath)
	}
}

func TestWhatABoxStandsFollowsWhetherItsProxyOwnsThePorts(t *testing.T) {
	t.Parallel()

	for name, front := range map[string]Front{"ocel's own proxy": {}, "a proxy routed by hand": routedByHand()} {
		owns := openFront(front, frontBox{}).Guarantees().OwnsPorts
		standsProxy := slices.ContainsFunc(ProxyItems(ArchAMD64, front), func(item Item) bool {
			return item.Kind == KindContainer && item.Name == caddy.Container
		})
		if standsProxy != owns {
			t.Errorf("%s: the box stands %s = %v, want %v: only a proxy that owns 80 and 443 is one ocel runs", name, caddy.Container, standsProxy, owns)
		}
		if published := len(switchboardOf(t, front).ports) > 0; published == owns {
			t.Errorf("%s: the switchboard publishes a port = %v, want it published only for a proxy that does not own the ports", name, published)
		}
		if recorded := front.recorded() != nil; recorded == owns {
			t.Errorf("%s: the record names a proxy = %v, want one named only for a proxy that does not own the ports", name, recorded)
		}
	}
}

func TestAProxyThisOcelDoesNotServeYetIsRefusedNamedRatherThanRunAsOcelsOwn(t *testing.T) {
	t.Parallel()

	for name, tc := range map[string]struct {
		front Front
		named string
	}{
		"Coolify's Traefik": {front: coolifysTraefik(), named: "Coolify's Traefik"},
		"your Caddy":        {front: Front{Caddy: &CaddyFront{Directory: "/etc/caddy/ocel.d", Config: "/etc/caddy/Caddyfile", Port: 8480}}, named: "your Caddy"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			front := openFront(tc.front, frontBox{})
			if _, builtin := front.(caddy.Builtin); builtin || front.Guarantees().OwnsPorts {
				t.Fatalf("%s opens as %T, want it never run as ocel's own proxy on 80 and 443", name, front)
			}
			if file := front.File(); file != "" {
				t.Errorf("%s names %s as its file, want none while it renders nothing, so no switchboard binds a directory for it", name, file)
			}
			ctx := context.Background()
			_, rendered := front.Render(proxy.Spec{})
			_, inspected := front.Inspect(ctx)
			_, certified := front.Certificate(ctx, "shop.example.com")
			for asked, err := range map[string]error{"Render": rendered, "Reload": front.Reload(ctx), "Inspect": inspected, "Certificate": certified} {
				if err == nil || !strings.Contains(err.Error(), tc.named) || !strings.Contains(err.Error(), "not supported yet") {
					t.Errorf("%s() on %s = %v, want it refused naming %s as not supported yet", asked, name, err, tc.named)
				}
			}
		})
	}
}
