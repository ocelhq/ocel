package host

import (
	"context"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/manual"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func routedByHand() Front { return Front{Manual: &ManualFront{Port: manual.DefaultPort}} }

func TestABoxFrontedByHandStandsNothingOfOcelsOwnProxy(t *testing.T) {
	t.Parallel()

	items := Items(providerkit.ClassProduction, []byte(aKey+"\n"), ArchAMD64, routedByHand())
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
	report := &said{}
	if _, err := NewConnector(stood.fronted(routedByHand())).Install(context.Background(), "box.example.com", []byte("a connector"), connectorConfig(), report); err != nil {
		t.Fatalf("Install() = %v", err)
	}
	if want := manual.Route("box.example.com", manual.DefaultPort); report.at(want) < 0 {
		t.Errorf("the install said %q, want %q: the console reaches the connector through your proxy", report.lines, want)
	}
}

func frontRecordItem(front Front, project string, class providerkit.Class) (Item, error) {
	return frontRecord{Proxy: front.recorded(), Project: project, Class: class}.item()
}

func recordOn(t *testing.T, stood *bench, class providerkit.Class, front Front, project string) {
	t.Helper()
	record, err := frontRecordItem(front, project, class)
	if err != nil {
		t.Fatal(err)
	}
	stood.stands[class] = append(unrecorded(stood, class), record)
}

func unrecorded(stood *bench, class providerkit.Class) []Item {
	return slices.DeleteFunc(stood.stands[class], func(item Item) bool { return item.Name == FrontRecordPath })
}

func TestABootstrapRecordsWhichProxyFrontsTheBoxAndWhoSetIt(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	stood := settledOn(t, class)
	stood.stands[class] = unrecorded(stood, class)
	if err := Bootstrap(stood.host(), testVendor, "shop").Apply(context.Background(),
		providerkit.BootstrapRequest{Class: class, Writer: "the-suite"}, nil); err != nil {
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

	class := providerkit.ClassPreview
	stood := settledOn(t, class)
	recordOn(t, stood, class, Front{}, "blog")
	if err := Bootstrap(stood.host(), testVendor, "shop").Apply(context.Background(),
		providerkit.BootstrapRequest{Class: class, Writer: "the-suite"}, nil); err != nil {
		t.Fatalf("Apply() = %v", err)
	}
	if at := stood.at("/dev/stdin " + quoted(FrontRecordPath)); at >= 0 {
		t.Errorf("the apply rewrote %s, which blog set and this project agrees with: %s", FrontRecordPath, stood.commands()[at])
	}
}

func TestABootstrapWhoseProxyTheBoxDoesNotRouteThroughIsRefusedWithWhatToWrite(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	stood := settledOn(t, class)
	recordOn(t, stood, class, routedByHand(), "blog")
	_, err := Bootstrap(stood.host(), testVendor, "shop").Plan(context.Background(), providerkit.BootstrapRequest{Class: class})
	refused := refusal(t, err, providerkit.CodeInvalid)
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
		"the box runs ocel's own proxy, the project routes by hand": {
			recorded: Front{}, ours: routedByHand(),
			wanted: []string{"ocel's own proxy", "set by blog/preview", "remove `\"proxy\"`"},
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			stood := settledOn(t, providerkit.ClassPreview)
			recordOn(t, stood, providerkit.ClassPreview, tc.recorded, "blog")
			refused := refusal(t, stood.fronted(tc.ours).FrontAgrees(context.Background()), providerkit.CodeInvalid)
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

	stood := settledOn(t, providerkit.ClassProduction)
	recordOn(t, stood, providerkit.ClassProduction, routedByHand(), "blog")
	if err := stood.fronted(routedByHand()).FrontAgrees(context.Background()); err != nil {
		t.Errorf("FrontAgrees() = %v, want a box that routes the way this project says let through", err)
	}
}

func TestADeployOntoABoxThatRecordsNoProxyIsSentToBootstrap(t *testing.T) {
	t.Parallel()

	stood := settledOn(t, providerkit.ClassProduction)
	stood.stands[providerkit.ClassProduction] = unrecorded(stood, providerkit.ClassProduction)
	refused := refusal(t, stood.host().FrontAgrees(context.Background()), providerkit.CodeNotReady)
	if !strings.Contains(refused.Message, FrontRecordPath) || !strings.Contains(refused.Message, "ocel bootstrap") {
		t.Errorf("the refusal says %q, want it to name %s and the bootstrap that writes it", refused.Message, FrontRecordPath)
	}
}

func TestTheLastClassToGoTakesTheRecordWithIt(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	stood := machine(map[providerkit.Class][]Item{class: bootstrapped(t, class)})
	recordOn(t, stood, class, Front{}, "shop")
	plan, err := Bootstrap(stood.host(), testVendor, "shop").PlanRemoval(context.Background(), class)
	if err != nil {
		t.Fatalf("PlanRemoval() = %v", err)
	}
	if !slices.ContainsFunc(plan.Groups[0].Changes, func(change providerkit.Change) bool {
		return change.Name == FrontRecordPath && change.Action == providerkit.ActionDelete
	}) {
		t.Errorf("the removal plan %+v leaves %s behind, and the next bootstrap would read it as another project's", plan.Groups[0].Changes, FrontRecordPath)
	}
}

func recordChange(t *testing.T, plan providerkit.Plan) providerkit.Change {
	t.Helper()
	for _, change := range plan.Groups[0].Changes {
		if change.Name == FrontRecordPath {
			return change
		}
	}
	t.Fatalf("the plan %+v says nothing of %s", plan.Groups[0].Changes, FrontRecordPath)
	return providerkit.Change{}
}

func TestABootstrapUnderAProxyRoutedByHandOntoABoxOcelsOwnProxyFrontsUnrecordedIsRefused(t *testing.T) {
	t.Parallel()

	class := providerkit.ClassProduction
	stood := settledOn(t, class)
	stood.stands[class] = unrecorded(stood, class)
	boot := Bootstrap(stood.fronted(routedByHand()), testVendor, "shop")
	_, planned := boot.Plan(context.Background(), providerkit.BootstrapRequest{Class: class})
	applied := boot.Apply(context.Background(), providerkit.BootstrapRequest{Class: class, Writer: "the-suite"}, nil)
	for step, err := range map[string]error{"Plan": planned, "Apply": applied} {
		refused := refusal(t, err, providerkit.CodeInvalid)
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

	class := providerkit.ClassProduction
	stood := settledOn(t, class)
	stood.stands[class] = unrecorded(stood, class)
	boot := Bootstrap(stood.host(), testVendor, "shop")
	described, err := boot.Describe(context.Background(), class)
	if err != nil {
		t.Fatalf("Describe() = %v", err)
	}
	if described.Stacks[0].DigestCurrent {
		t.Error("Describe() calls a box whose record was deleted current, so no bootstrap would ever write it back and every deploy refuses")
	}
	plan, err := boot.Plan(context.Background(), providerkit.BootstrapRequest{Class: class})
	if err != nil {
		t.Fatalf("Plan() = %v", err)
	}
	if plan.Groups[0].Action != providerkit.ActionUpdate {
		t.Errorf("the plan acts %q on a box missing its record, want %q", plan.Groups[0].Action, providerkit.ActionUpdate)
	}
	if change := recordChange(t, plan); change.Action != providerkit.ActionCreate {
		t.Errorf("the plan %s %s, want it created", change.Action, FrontRecordPath)
	}
}

func TestABootstrapOfAFreshBoxUnderAProxyRoutedByHandPlansItsRecord(t *testing.T) {
	t.Parallel()

	fresh := machine(nil)
	fresh.answer = func(command string) (session.Result, bool) {
		return session.Result{Stdout: aKey + "\n"}, command == "cat ~/.ssh/authorized_keys 2>/dev/null"
	}
	plan, err := Bootstrap(fresh.fronted(routedByHand()), testVendor, "shop").Plan(context.Background(),
		providerkit.BootstrapRequest{Class: providerkit.ClassProduction})
	if err != nil {
		t.Fatalf("Plan() = %v, want a fresh box, where nothing of ocel's stands, free to take a proxy routed by hand", err)
	}
	if change := recordChange(t, plan); change.Action != providerkit.ActionCreate {
		t.Errorf("the plan %s %s on a fresh box, want it created", change.Action, FrontRecordPath)
	}
}
