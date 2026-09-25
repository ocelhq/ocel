package caddy_test

import (
	"bytes"
	"encoding/json"
	"slices"
	"strings"
	"testing"

	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
)

const switchboard = "ocel-switchboard:8080"

func admitting(entries ...proxy.Entry) proxy.Admission {
	return proxy.Admission{Entries: entries, Upstream: switchboard}
}

type rendered struct {
	Admin struct {
		Listen string `json:"listen"`
	} `json:"admin"`
	Logging json.RawMessage `json:"logging"`
	Apps    struct {
		HTTP struct {
			GracePeriod string `json:"grace_period"`
			Servers     map[string]struct {
				Listen    []string `json:"listen"`
				Automatic *struct {
					Skip []string `json:"skip_certificates"`
				} `json:"automatic_https"`
				Routes []struct {
					Match []struct {
						Host []string `json:"host"`
					} `json:"match"`
					Handle []map[string]any `json:"handle"`
				} `json:"routes"`
			} `json:"servers"`
		} `json:"http"`
		TLS *struct {
			Certificates struct {
				LoadFiles []struct {
					Certificate string   `json:"certificate"`
					Key         string   `json:"key"`
					Tags        []string `json:"tags"`
				} `json:"load_files"`
			} `json:"certificates"`
		} `json:"tls"`
	} `json:"apps"`
}

func render(t *testing.T, admission proxy.Admission) ([]byte, rendered) {
	t.Helper()
	written, err := caddy.Render(admission)
	if err != nil {
		t.Fatalf("Render() = %v", err)
	}
	var read rendered
	if err := json.Unmarshal(written, &read); err != nil {
		t.Fatal(err)
	}
	if len(read.Apps.HTTP.Servers) != 1 {
		t.Fatalf("the config declares %d servers, want the one front server", len(read.Apps.HTTP.Servers))
	}
	return written, read
}

func front(read rendered) (hosts []string, forwards []map[string]any, catchAll bool) {
	for _, server := range read.Apps.HTTP.Servers {
		for _, route := range server.Routes {
			if len(route.Match) == 0 {
				catchAll = true
			}
			for _, matched := range route.Match {
				hosts = append(hosts, matched.Host...)
			}
			forwards = append(forwards, route.Handle...)
		}
	}
	return hosts, forwards, catchAll
}

func TestTheFrontProxyHoldsACertificateForEveryHostnameAdmittedAndForwardsEverythingToTheSwitchboard(t *testing.T) {
	t.Parallel()

	_, read := render(t, admitting(proxy.Entry{Hostname: "shop.example.com"}, proxy.Entry{Hostname: "Box.Example.com"}))
	hosts, forwards, catchAll := front(read)
	if !slices.Equal(hosts, []string{"box.example.com", "shop.example.com"}) {
		t.Errorf("the front proxy matches %v, want exactly the admitted hostnames: a host matcher is what caddy orders a certificate for", hosts)
	}
	if !catchAll {
		t.Error("nothing forwards a hostname the box does not claim, so caddy answers it with an empty 200 rather than the switchboard's 404 naming the box")
	}
	if len(forwards) != 2 {
		t.Fatalf("the front proxy runs %d handlers, want one forward per route", len(forwards))
	}
	for _, handler := range forwards {
		upstreams, _ := json.Marshal(handler["upstreams"])
		if handler["handler"] != "reverse_proxy" || string(upstreams) != `[{"dial":"`+switchboard+`"}]` {
			t.Errorf("a route runs %v, want a forward to %s and nothing else: routing belongs to the switchboard", handler, switchboard)
		}
		for _, header := range []string{"headers", "trusted_proxies"} {
			if _, set := handler[header]; set {
				t.Errorf("the forward sets %s, want caddy's own: it passes Host through and states X-Forwarded-Proto from the connection it terminated", header)
			}
		}
	}
}

func TestAReloadLeavesEveryStreamTheGraceItTakesRatherThanCuttingIt(t *testing.T) {
	t.Parallel()

	_, read := render(t, admitting(proxy.Entry{Hostname: "shop.example.com"}))
	if read.Apps.HTTP.GracePeriod != "30s" {
		t.Errorf("the config declares a grace period of %q, want 30s: caddy's default is eternal", read.Apps.HTTP.GracePeriod)
	}
	_, forwards, _ := front(read)
	for _, handler := range forwards {
		if handler["stream_close_delay"] != "30s" {
			t.Errorf("a forward closes its streams after %v, want 30s: caddy closes every websocket a reverse_proxy holds the moment a reload unloads it", handler["stream_close_delay"])
		}
	}
}

func TestWhatARenderSaysDependsOnWhatIsAdmittedAndNotOnTheOrderItCameIn(t *testing.T) {
	t.Parallel()

	one, _ := render(t, admitting(proxy.Entry{Hostname: "b.example.com"}, proxy.Entry{Hostname: "a.example.com", Pin: caddy.PinsDir + "/a"}))
	two, _ := render(t, admitting(proxy.Entry{Hostname: "a.example.com", Pin: caddy.PinsDir + "/a"}, proxy.Entry{Hostname: "b.example.com"}))
	if !bytes.Equal(one, two) {
		t.Error("two renders of the same hostnames differ by the order they were handed in, and a reshape that changes no hostname would reload caddy")
	}
}

func TestThePreviewWildcardIsServedWithoutACertificateAndItsProbeWithOne(t *testing.T) {
	t.Parallel()

	const base = "preview.example.com"
	wildcard := edge.PreviewWildcard(base)
	admission := admitting()
	admission.PreviewBase = base
	_, read := render(t, admission)
	hosts, _, _ := front(read)
	if !slices.Contains(hosts, wildcard) || !slices.Contains(hosts, edge.ProbeHostname(wildcard)) {
		t.Errorf("the front proxy matches %v, want %s and %s among them", hosts, wildcard, edge.ProbeHostname(wildcard))
	}
	for _, server := range read.Apps.HTTP.Servers {
		if server.Automatic == nil || !slices.Equal(server.Automatic.Skip, []string{wildcard}) {
			t.Errorf("the config skips certificates for %+v, want exactly [%s]: a wildcard order needs a dns-01 module the box has none of, and the probe beside it must hold a certificate for its https answer to be read", server.Automatic, wildcard)
		}
	}
	_, read = render(t, admitting(proxy.Entry{Hostname: "shop.example.com"}))
	for _, server := range read.Apps.HTTP.Servers {
		if server.Automatic != nil {
			t.Errorf("a box with no preview entry skips %v, want nothing skipped", server.Automatic.Skip)
		}
	}
}

func TestAPinnedPairIsLoadedOnceOffTheDirectoryTheProxyMountsForEveryHostnameItCovers(t *testing.T) {
	t.Parallel()

	pin := caddy.PinsDir + "/wild"
	_, read := render(t, admitting(
		proxy.Entry{Hostname: "shop.example.com", Pin: pin},
		proxy.Entry{Hostname: "blog.example.com", Pin: pin},
		proxy.Entry{Hostname: "api.example.com"},
	))
	if read.Apps.TLS == nil || len(read.Apps.TLS.Certificates.LoadFiles) != 1 {
		t.Fatalf("the config loads %+v, want the one pinned pair", read.Apps.TLS)
	}
	loaded := read.Apps.TLS.Certificates.LoadFiles[0]
	if loaded.Certificate != caddy.PinCertificate(caddy.PinsMount+"/wild") || loaded.Key != caddy.PinKey(caddy.PinsMount+"/wild") {
		t.Errorf("the pair is loaded from %s and %s, want the path the pin stands at inside the proxy", loaded.Certificate, loaded.Key)
	}
	if !slices.Equal(loaded.Tags, []string{"blog.example.com", "shop.example.com"}) {
		t.Errorf("the pair is tagged %v, want each hostname it serves", loaded.Tags)
	}
	_, read = render(t, admitting(proxy.Entry{Hostname: "shop.example.com"}))
	if read.Apps.TLS != nil {
		t.Errorf("a box pinning nothing loads %+v", read.Apps.TLS)
	}
}

func TestWhatTheProxyCouldNotHoldIsRefusedRatherThanRendered(t *testing.T) {
	t.Parallel()

	for what, admission := range map[string]proxy.Admission{
		"no upstream":                    {Entries: []proxy.Entry{{Hostname: "shop.example.com"}}},
		"a wildcard hostname":            admitting(proxy.Entry{Hostname: "*.example.com"}),
		"an empty hostname":              admitting(proxy.Entry{}),
		"a pin outside the pin root":     admitting(proxy.Entry{Hostname: "shop.example.com", Pin: "/etc/shadow"}),
		"a pin beneath the pin root":     admitting(proxy.Entry{Hostname: "shop.example.com", Pin: caddy.PinsDir + "/nested/shop"}),
		"the pin root itself":            admitting(proxy.Entry{Hostname: "shop.example.com", Pin: caddy.PinsDir}),
		"a pin climbing out of its root": admitting(proxy.Entry{Hostname: "shop.example.com", Pin: caddy.PinsDir + "/.."}),
	} {
		if written, err := caddy.Render(admission); err == nil {
			t.Errorf("an admission with %s rendered:\n%s", what, written)
		}
	}
}

func TestTheAdminApiIsReachedOverItsSocketAloneAndNoConfigCanOrderCertificatesOnDemand(t *testing.T) {
	t.Parallel()

	admission := admitting(proxy.Entry{Hostname: "shop.example.com", Pin: caddy.PinsDir + "/shop"})
	admission.PreviewBase = "preview.example.com"
	written, read := render(t, admission)
	if read.Admin.Listen != "unix/"+caddy.AdminSocket+"|0600" {
		t.Errorf("the admin endpoint listens at %q, want the socket only root inside the proxy reaches", read.Admin.Listen)
	}
	if strings.Contains(string(written), "on_demand") || caddy.Foreign(written) != "" {
		t.Errorf("the rendered config can order certificates for names nothing admitted:\n%s", written)
	}
	for foreign, document := range map[string]string{
		"an automation policy": `{"apps":{"tls":{"automation":{"on_demand":{}}}}}`,
		"a config loader":      `{"admin":{"config":{"load":{"module":"http"}}}}`,
	} {
		if caddy.Foreign([]byte(document)) == "" {
			t.Errorf("a config declaring %s reads as one ocel renders", foreign)
		}
	}
}

func TestTheAccessLogKeepsThePathAndRedactsTheQuery(t *testing.T) {
	t.Parallel()

	_, read := render(t, admitting(proxy.Entry{Hostname: "shop.example.com"}))
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
