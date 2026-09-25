package caddy_test

import (
	"bytes"
	"encoding/json"
	"maps"
	"net/http"
	"net/netip"
	"net/url"
	"slices"
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

const (
	switchboard = "ocel-switchboard:8080"
	edgeName    = "box"
)

var permission = proxy.Permission{Dial: "unix//run/ocel-front/admit.sock", Path: "/admit"}

func pinned(hostname, leaf string) proxy.Pin {
	return proxy.Pin{Hostname: hostname, Path: caddy.PinsDir + "/" + leaf}
}

func specified(pins ...proxy.Pin) proxy.Spec {
	return proxy.Spec{Pins: pins, Upstream: switchboard, Edge: edgeName, Permission: permission}
}

type policy struct {
	Subjects []string         `json:"subjects"`
	Issuers  []map[string]any `json:"issuers"`
	OnDemand bool             `json:"on_demand"`
}

type server struct {
	Listen    []string          `json:"listen"`
	Automatic json.RawMessage   `json:"automatic_https"`
	Policies  []json.RawMessage `json:"tls_connection_policies"`
	Routes    []struct {
		Match  []json.RawMessage `json:"match"`
		Handle []map[string]any  `json:"handle"`
	} `json:"routes"`
	Errors *struct {
		Routes []struct {
			Match  json.RawMessage  `json:"match"`
			Handle []map[string]any `json:"handle"`
		} `json:"routes"`
	} `json:"errors"`
}

type rendered struct {
	Admin struct {
		Listen string `json:"listen"`
	} `json:"admin"`
	Logging json.RawMessage `json:"logging"`
	Apps    struct {
		HTTP struct {
			GracePeriod string            `json:"grace_period"`
			Servers     map[string]server `json:"servers"`
		} `json:"http"`
		TLS *struct {
			Certificates *struct {
				LoadFiles []struct {
					Certificate string   `json:"certificate"`
					Key         string   `json:"key"`
					Tags        []string `json:"tags"`
				} `json:"load_files"`
			} `json:"certificates"`
			Automation struct {
				Policies []policy `json:"policies"`
				OnDemand struct {
					Ask        string            `json:"ask"`
					Permission map[string]string `json:"permission"`
				} `json:"on_demand"`
			} `json:"automation"`
		} `json:"tls"`
	} `json:"apps"`
}

func (r rendered) front() server { return r.Apps.HTTP.Servers["ocel"] }

func render(t *testing.T, spec proxy.Spec) ([]byte, rendered) {
	t.Helper()
	written, err := (caddy.Builtin{}).Render(spec)
	if err != nil {
		t.Fatalf("Render() = %v", err)
	}
	var read rendered
	if err := json.Unmarshal(written, &read); err != nil {
		t.Fatal(err)
	}
	if _, front := read.Apps.HTTP.Servers["ocel"]; !front || len(read.Apps.HTTP.Servers) != 2 {
		t.Fatalf("the config declares servers %v, want the front server and the relay to the switchboard's admission", slices.Sorted(maps.Keys(read.Apps.HTTP.Servers)))
	}
	if read.Apps.TLS == nil {
		t.Fatalf("the config declares no tls app, so nothing on :443 is ever issued a certificate:\n%s", written)
	}
	return written, read
}

func TestTheFrontProxyNamesNoHostnameAndForwardsEverythingToTheSwitchboard(t *testing.T) {
	t.Parallel()

	written, read := render(t, specified())
	if strings.Contains(string(written), `"host"`) {
		t.Errorf("the config matches on a host, and every hostname it names is a reload the day that hostname is bound or unbound:\n%s", written)
	}
	for _, server := range []server{read.front()} {
		if len(server.Routes) != 1 || len(server.Routes[0].Match) != 0 || len(server.Routes[0].Handle) != 1 {
			t.Fatalf("the front server runs routes %+v, want one catch-all forward", server.Routes)
		}
		handler := server.Routes[0].Handle[0]
		upstreams, _ := json.Marshal(handler["upstreams"])
		if handler["handler"] != "reverse_proxy" || string(upstreams) != `[{"dial":"`+switchboard+`"}]` {
			t.Errorf("the route runs %v, want a forward to %s and nothing else: routing belongs to the switchboard", handler, switchboard)
		}
		for _, header := range []string{"headers", "trusted_proxies"} {
			if _, set := handler[header]; set {
				t.Errorf("the forward sets %s, want caddy's own: it passes Host through and states X-Forwarded-Proto from the connection it terminated", header)
			}
		}
		if len(server.Policies) != 1 || string(server.Policies[0]) != "{}" {
			t.Errorf("the front server declares connection policies %s, want exactly one empty policy: with no host matcher, caddy terminates tls on :443 only for a server that declares one", server.Policies)
		}
		if len(server.Automatic) != 0 {
			t.Errorf("the front server declares automatic_https %s, want none: with no hostname named there is nothing to skip", server.Automatic)
		}
	}
}

func TestTheProxyOrdersOnDemandOnlyWhatTheSwitchboardAdmits(t *testing.T) {
	t.Parallel()

	_, read := render(t, specified())
	onDemand := read.Apps.TLS.Automation.OnDemand
	if onDemand.Ask != "" || onDemand.Permission["module"] != "http" || onDemand.Permission["endpoint"] != caddy.PermissionEndpoint(permission.Path) || len(onDemand.Permission) != 2 {
		t.Errorf("on-demand issuance asks %q through %v, want the relay to the switchboard's admission at %s through the http permission module and nothing else", onDemand.Ask, onDemand.Permission, caddy.PermissionEndpoint(permission.Path))
	}
	policies := read.Apps.TLS.Automation.Policies
	if len(policies) != 2 {
		t.Fatalf("the config declares %d automation policies, want the internal names' and the catch-all", len(policies))
	}
	for _, held := range policies {
		if !held.OnDemand {
			t.Errorf("the policy for %v orders ahead of any handshake, want every order on demand: a policy that is not on demand holds a hostname list that changes with every bind", held.Subjects)
		}
	}
	internal, public := policies[0], policies[1]
	if len(public.Subjects) != 0 || len(public.Issuers) != 0 {
		t.Errorf("the catch-all policy is %+v, want no subjects and caddy's default issuer: an issuer list carrying the internal CA falls through to it for a public name whose order failed, and serves it self-signed for hours", public)
	}
	if len(internal.Issuers) != 1 || internal.Issuers[0]["module"] != "internal" || len(internal.Issuers[0]) != 1 {
		t.Errorf("the policy for names no public CA issues is issued by %v, want caddy's internal CA alone", internal.Issuers)
	}
	for _, name := range []string{
		"localhost",
		"ocel-vps-e2e.localhost",
		"ocel-edge-probe.preview.ocel-vps-e2e.localhost",
		"shop--pr-7--web.preview.ocel-vps-e2e.localhost",
		"ocel-edge-probe.preview.ocel.home.arpa",
		"printer.local",
		"db.corp.internal",
	} {
		if !slices.ContainsFunc(internal.Subjects, func(subject string) bool { return labelwise(name, subject) }) {
			t.Errorf("%s is under no subject of the internal policy, so it reaches the catch-all and an ACME order no CA will take", name)
		}
	}
	for _, name := range []string{"shop.example.com", "localhost.example.com", "local.example.com"} {
		if slices.ContainsFunc(internal.Subjects, func(subject string) bool { return labelwise(name, subject) }) {
			t.Errorf("%s is issued by the internal CA, and a browser trusts none of what it serves", name)
		}
	}
}

func labelwise(name, subject string) bool {
	named, pattern := strings.Split(name, "."), strings.Split(subject, ".")
	if len(named) != len(pattern) {
		return false
	}
	for at := range pattern {
		if pattern[at] != "*" && pattern[at] != named[at] {
			return false
		}
	}
	return true
}

func TestEveryErrorTheFrontProxyAnswersItselfNamesTheEdge(t *testing.T) {
	t.Parallel()

	_, read := render(t, specified())
	for _, server := range []server{read.front()} {
		if server.Errors == nil || len(server.Errors.Routes) != 1 || server.Errors.Routes[0].Match != nil || len(server.Errors.Routes[0].Handle) != 1 {
			t.Fatalf("the front server handles its own errors with %+v, want one route answering every error", server.Errors)
		}
		answered, _ := json.Marshal(server.Errors.Routes[0].Handle[0])
		if want := `{"handler":"static_response","headers":{"` + http.CanonicalHeaderKey(edge.HeaderEdge) + `":["` + edgeName + `"]},"status_code":"{http.error.status_code}"}`; string(answered) != want {
			t.Errorf("the front proxy answers its own errors with %s, want %s: caddy answers a 502 of its own while the switchboard is down or being recreated, and the bind's probe reads the edge off every answer the box gives", answered, want)
		}
	}
}

func TestAReloadLeavesEveryStreamTheGraceItTakesRatherThanCuttingIt(t *testing.T) {
	t.Parallel()

	_, read := render(t, specified())
	if read.Apps.HTTP.GracePeriod != "30s" {
		t.Errorf("the config declares a grace period of %q, want 30s: caddy's default is eternal", read.Apps.HTTP.GracePeriod)
	}
	for _, server := range []server{read.front()} {
		for _, route := range server.Routes {
			for _, handler := range route.Handle {
				if handler["stream_close_delay"] != "30s" {
					t.Errorf("a forward closes its streams after %v, want 30s: caddy closes every websocket a reverse_proxy holds the moment a reload unloads it", handler["stream_close_delay"])
				}
			}
		}
	}
}

func TestWhatARenderSaysDependsOnWhichPairsArePinnedAndNotOnTheOrderTheyCameIn(t *testing.T) {
	t.Parallel()

	one, _ := render(t, specified(pinned("b.example.com", "b"), pinned("a.example.com", "a"), pinned("b.example.com", "b")))
	two, _ := render(t, specified(pinned("a.example.com", "a"), pinned("b.example.com", "b")))
	if !bytes.Equal(one, two) {
		t.Error("two renders of the same pins differ by the order they were handed in, and a reshape that changes no pin would reload caddy")
	}
	three, _ := render(t, specified(pinned("a.example.com", "a")))
	if bytes.Equal(one, three) {
		t.Error("a render carrying one pin fewer says the same, so the running proxy keeps serving a pair the operator took away")
	}
}

func TestEveryPinnedPairIsLoadedOnceOffTheDirectoryTheProxyMounts(t *testing.T) {
	t.Parallel()

	_, read := render(t, specified(pinned("*.example.com", "wild"), pinned("shop.example.com", "shop"), pinned("*.shop.example.com", "wild")))
	if read.Apps.TLS.Certificates == nil || len(read.Apps.TLS.Certificates.LoadFiles) != 2 {
		t.Fatalf("the config loads %+v, want each pinned pair once", read.Apps.TLS.Certificates)
	}
	for at, leaf := range []string{"shop", "wild"} {
		loaded := read.Apps.TLS.Certificates.LoadFiles[at]
		mounted := caddy.PinsMount + "/" + leaf
		if loaded.Certificate != caddy.PinCertificate(mounted) || loaded.Key != caddy.PinKey(mounted) {
			t.Errorf("the pair pinned at %s is loaded from %s and %s, want the path it stands at inside the proxy", leaf, loaded.Certificate, loaded.Key)
		}
		if !slices.Equal(loaded.Tags, []string{mounted}) {
			t.Errorf("the pair pinned at %s is tagged %v, want its own path and nothing else: a tag is how a handshake is handed this pair, and one that names a claimed hostname changes the config with every bind", leaf, loaded.Tags)
		}
	}
	_, read = render(t, specified())
	if read.Apps.TLS.Certificates != nil {
		t.Errorf("a box pinning nothing loads %+v", read.Apps.TLS.Certificates)
	}
}

type connectionPolicy struct {
	Match     map[string][]string `json:"match"`
	Selection map[string][]string `json:"certificate_selection"`
}

func connectionPolicies(t *testing.T, read rendered) []connectionPolicy {
	t.Helper()
	var policies []connectionPolicy
	for _, server := range read.Apps.HTTP.Servers {
		for _, held := range server.Policies {
			var policy connectionPolicy
			if err := json.Unmarshal(held, &policy); err != nil {
				t.Fatalf("read the connection policy %s: %v", held, err)
			}
			policies = append(policies, policy)
		}
	}
	if len(policies) == 0 {
		t.Fatal("the front server declares no connection policy, so caddy terminates no tls on :443 for a config that names no host")
	}
	return policies
}

func TestAHandshakeForAPinnedNameIsHandedItsPinAheadOfAnythingOrderedOnDemand(t *testing.T) {
	t.Parallel()

	_, read := render(t, specified(pinned("*.Example.com", "wild"), pinned("shop.example.com", "shop"), pinned("api.example.com", "wild")))
	policies := connectionPolicies(t, read)
	type handed struct{ sni, tag string }
	var got []handed
	for _, policy := range policies[:len(policies)-1] {
		if len(policy.Match) != 1 || len(policy.Match["sni"]) != 1 || len(policy.Selection) != 1 || len(policy.Selection["any_tag"]) != 1 {
			t.Fatalf("a pin's connection policy is %+v, want one sni matched and one tag selected", policy)
		}
		got = append(got, handed{policy.Match["sni"][0], policy.Selection["any_tag"][0]})
	}
	want := []handed{
		{"api.example.com", caddy.PinsMount + "/wild"},
		{"shop.example.com", caddy.PinsMount + "/shop"},
		{"*.example.com", caddy.PinsMount + "/wild"},
	}
	if !slices.Equal(got, want) {
		t.Errorf("the pins hand handshakes %v, want %v: caddy takes the first policy whose sni matches, and a name pinned exactly is served off that pin ahead of any wildcard, as the removal plan reads it", got, want)
	}
	if last := policies[len(policies)-1]; last.Match != nil || last.Selection != nil {
		t.Errorf("the last connection policy is %+v, want the catch-all that serves whatever the proxy ordered on demand", last)
	}
	_, read = render(t, specified())
	if policies := connectionPolicies(t, read); len(policies) != 1 || policies[0].Match != nil || policies[0].Selection != nil {
		t.Errorf("a box pinning nothing declares connection policies %+v, want the one empty policy that turns tls on without naming a host", policies)
	}
}

func TestWhatTheProxyCouldNotHoldIsRefusedRatherThanRendered(t *testing.T) {
	t.Parallel()

	for what, spec := range map[string]proxy.Spec{
		"no upstream":                     {Edge: edgeName, Permission: permission},
		"no edge to name":                 {Upstream: switchboard, Permission: permission},
		"no permission endpoint":          {Upstream: switchboard, Edge: edgeName},
		"no admission to relay to":        {Upstream: switchboard, Edge: edgeName, Permission: proxy.Permission{Path: permission.Path}},
		"no path to ask the admission at": {Upstream: switchboard, Edge: edgeName, Permission: proxy.Permission{Dial: permission.Dial}},
		"a pin outside the pin root":      specified(proxy.Pin{Hostname: "shop.example.com", Path: "/etc/shadow"}),
		"a pin beneath the pin root":      specified(pinned("shop.example.com", "nested/shop")),
		"the pin root itself":             specified(proxy.Pin{Hostname: "shop.example.com", Path: caddy.PinsDir}),
		"a pin climbing out of its root":  specified(pinned("shop.example.com", "..")),
		"a pin naming no hostname":        specified(pinned("", "shop")),
	} {
		if written, err := (caddy.Builtin{}).Render(spec); err == nil {
			t.Errorf("a spec with %s rendered:\n%s", what, written)
		}
	}
}

func TestTheAdminApiIsReachedOverItsSocketAloneAndNoConfigOrdersWithoutTheSwitchboardsWord(t *testing.T) {
	t.Parallel()

	written, read := render(t, specified(pinned("shop.example.com", "shop")))
	if read.Admin.Listen != "unix/"+caddy.AdminSocket+"|0600" {
		t.Errorf("the admin endpoint listens at %q, want the socket only root inside the proxy reaches", read.Admin.Listen)
	}
	if foreign := (caddy.Builtin{}).Unrendered(written, permission); foreign != "" {
		t.Errorf("the rendered config reads as declaring %s", foreign)
	}
	if foreign := (caddy.Builtin{}).Unrendered([]byte(`{"apps":{"http":{}}}`), permission); foreign != "" {
		t.Errorf("a config ordering nothing at all reads as declaring %s: it is stale, not dangerous", foreign)
	}

	alteredApp := func(app string, change func(held map[string]any)) []byte {
		var copied map[string]any
		if err := json.Unmarshal(written, &copied); err != nil {
			t.Fatal(err)
		}
		change(copied["apps"].(map[string]any)[app].(map[string]any))
		said, err := json.Marshal(copied)
		if err != nil {
			t.Fatal(err)
		}
		return said
	}
	altered := func(change func(automation map[string]any)) []byte {
		return alteredApp("tls", func(held map[string]any) { change(held["automation"].(map[string]any)) })
	}
	relay := func(held map[string]any) map[string]any {
		return held["servers"].(map[string]any)["admit"].(map[string]any)
	}
	onDemand := func(automation map[string]any) map[string]any { return automation["on_demand"].(map[string]any) }
	for foreign, config := range map[string][]byte{
		"a permission endpoint that is not the switchboard's": altered(func(a map[string]any) {
			onDemand(a)["permission"] = map[string]any{"module": "http", "endpoint": "http://attacker.example.com/admit"}
		}),
		"no permission module": altered(func(a map[string]any) { delete(onDemand(a), "permission") }),
		"an ask beside the permission": altered(func(a map[string]any) {
			onDemand(a)["ask"] = "http://127.0.0.1:9/ask"
		}),
		"a policy ordering ahead of any handshake": altered(func(a map[string]any) {
			a["policies"] = append(a["policies"].([]any), map[string]any{"subjects": []any{"shop.example.com"}})
		}),
		"a catch-all issued by the internal CA too": altered(func(a map[string]any) {
			policies := a["policies"].([]any)
			policies[len(policies)-1].(map[string]any)["issuers"] = []any{map[string]any{"module": "acme"}, map[string]any{"module": "internal"}}
		}),
		"a config loader": []byte(`{"admin":{"config":{"load":{"module":"http"}}}}`),
	} {
		if (caddy.Builtin{}).Unrendered(config, permission) == "" {
			t.Errorf("a config declaring %s reads as one ocel renders", foreign)
		}
	}

	endpoint, err := url.Parse(caddy.PermissionEndpoint(permission.Path))
	if err != nil {
		t.Fatal(err)
	}
	for foreign, config := range map[string][]byte{
		"a relay to an admission that is not the switchboard's": alteredApp("http", func(h map[string]any) {
			relay(h)["routes"] = []any{map[string]any{"handle": []any{map[string]any{"handler": "static_response", "status_code": 200}}}}
		}),
		"no relay to the admission": alteredApp("http", func(h map[string]any) {
			delete(h["servers"].(map[string]any), "admit")
		}),
		"a relay reached beyond the proxy's own loopback": alteredApp("http", func(h map[string]any) {
			relay(h)["listen"] = []any{":2020"}
		}),
	} {
		said := (caddy.Builtin{}).Unrendered(config, permission)
		if !strings.Contains(said, endpoint.Host) || strings.Contains(said, "automation") {
			t.Errorf("a config declaring %s reads as declaring %q, want the refusal to name the relay at %s: its tls automation is the one ocel renders", foreign, said, endpoint.Host)
		}
	}
}

func TestTheProxyAsksTheSwitchboardsAdmissionThroughARelayOnlyItsOwnLoopbackReaches(t *testing.T) {
	t.Parallel()

	_, read := render(t, specified())
	endpoint, err := url.Parse(caddy.PermissionEndpoint(permission.Path))
	if err != nil {
		t.Fatal(err)
	}
	if address, err := netip.ParseAddrPort(endpoint.Host); err != nil || !address.Addr().IsLoopback() || endpoint.Path != permission.Path {
		t.Fatalf("the proxy asks %s, want a loopback address inside the proxy at the admission's path %s: caddy asks over tcp alone, and the admission answers only over the socket the front proxy holds", endpoint, permission.Path)
	}
	for name, held := range read.Apps.HTTP.Servers {
		if name == "ocel" {
			continue
		}
		if !slices.Equal(held.Listen, []string{endpoint.Host}) {
			t.Errorf("the relay listens on %v, want %s alone: whoever reaches it is answered as the front proxy", held.Listen, endpoint.Host)
		}
		if len(held.Routes) != 1 || len(held.Routes[0].Match) != 0 || len(held.Routes[0].Handle) != 1 {
			t.Fatalf("the relay runs routes %+v, want one forward", held.Routes)
		}
		upstreams, _ := json.Marshal(held.Routes[0].Handle[0]["upstreams"])
		if held.Routes[0].Handle[0]["handler"] != "reverse_proxy" || string(upstreams) != `[{"dial":"`+permission.Dial+`"}]` {
			t.Errorf("the relay runs %v, want a forward to %s and nothing else", held.Routes[0].Handle[0], permission.Dial)
		}
		if len(held.Policies) != 0 || held.Errors != nil {
			t.Errorf("the relay declares tls policies %s and errors %+v, want neither: it answers the proxy in plain http what the admission said", held.Policies, held.Errors)
		}
	}
}

func TestTheAccessLogKeepsThePathAndRedactsTheQuery(t *testing.T) {
	t.Parallel()

	_, read := render(t, specified())
	var logging struct {
		Logs map[string]struct {
			Encoder struct {
				Fields map[string]struct {
					Filter string `json:"filter"`
					Regexp string `json:"regexp"`
					Value  string `json:"value"`
				} `json:"fields"`
			} `json:"encoder"`
		} `json:"logs"`
	}
	if err := json.Unmarshal(read.Logging, &logging); err != nil {
		t.Fatal(err)
	}
	uri := logging.Logs["ocel"].Encoder.Fields["request>uri"]
	if uri.Filter != "regexp" || uri.Regexp != `\?.*$` || uri.Value != "?redacted" {
		t.Errorf("the access log writes the request uri through %+v, want the query string replaced and the path kept: a query carries tokens", uri)
	}
}
