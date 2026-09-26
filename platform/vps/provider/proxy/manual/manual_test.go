package manual_test

import (
	"context"
	"errors"
	"net/netip"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/certs"
	"github.com/ocelhq/ocel/platform/vps/provider/listeners"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/caddy"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy/manual"
)

type box struct {
	asked      []string
	listening  []listeners.Listener
	publishing []string
	claimed    []string
	answers    map[string]string
	unreached  map[string]string
	unread     error
}

func (b *box) Listening(context.Context) ([]listeners.Listener, error) {
	b.asked = append(b.asked, "listening")
	return b.listening, b.unread
}

func (b *box) Publishing(_ context.Context, port string) ([]string, error) {
	b.asked = append(b.asked, "publishing "+port)
	return b.publishing, nil
}

func (b *box) Claimed(context.Context) ([]string, error) {
	b.asked = append(b.asked, "claimed")
	return b.claimed, nil
}

func (b *box) Probe(_ context.Context, hostname string) (string, string, error) {
	b.asked = append(b.asked, "probe "+hostname)
	return b.answers[hostname], b.unreached[hostname], nil
}

func on443() []listeners.Listener {
	return []listeners.Listener{{Addr: netip.IPv4Unspecified(), Port: 443}}
}

func TestManualIsRenderedIntoNoFile(t *testing.T) {
	t.Parallel()

	if got := (manual.Manual{}).File(); got != "" {
		t.Errorf("File() = %q, want none: ocel writes nothing to a proxy you route by hand", got)
	}
}

func TestManualGuaranteesNothingOfWhatItsProxyDoes(t *testing.T) {
	t.Parallel()

	if got := (manual.Manual{}).Guarantees(); got != (proxy.Guarantees{}) {
		t.Errorf("Guarantees() = %+v, want every guarantee false: the proxy is yours", got)
	}
}

func TestManualRendersNothingForItsProxyWhateverTheBoxAdmits(t *testing.T) {
	t.Parallel()

	front := manual.Manual{Box: &box{}}
	spec := proxy.Spec{
		Upstream:   "ocel-switchboard:8080",
		Edge:       "box",
		Permission: proxy.Permission{Dial: "/run/ocel-front/admit.sock", Path: "/admit"},
	}
	rendered, err := front.Render(spec)
	if err != nil || rendered != nil {
		t.Errorf("Render() = %q, %v; want nothing to write", rendered, err)
	}
	if declared := front.Unrendered([]byte(`{"apps":{}}`), spec.Permission); declared != "" {
		t.Errorf("Unrendered() = %q, want nothing: ocel renders no config it could spot a stranger in", declared)
	}
}

func TestManualTouchesNothingToReload(t *testing.T) {
	t.Parallel()

	machine := &box{}
	front := manual.Manual{Box: machine}
	if err := front.Reload(context.Background()); err != nil {
		t.Errorf("Reload() = %v, want nothing to do", err)
	}
	if len(machine.asked) != 0 {
		t.Errorf("Reload asked the box %q, want nothing asked", machine.asked)
	}
}

func TestManualSaysYourProxyRenewsACertificate(t *testing.T) {
	t.Parallel()

	certificate, err := (manual.Manual{Box: &box{}}).Certificate(context.Background(), "shop.example.com")
	if err != nil {
		t.Fatalf("Certificate() = %v", err)
	}
	if certificate.Renewal != certs.AdoptedRenewal || certificate.Trouble != nil {
		t.Errorf("Certificate() = %+v, want renewal %q and no rate limit read", certificate, certs.AdoptedRenewal)
	}
}

func verdicts(inspected proxy.Checks) map[string]provider.HostCheck {
	bySubject := map[string]provider.HostCheck{}
	for _, check := range inspected {
		bySubject[check.Subject] = check
	}
	return bySubject
}

func TestManualPassesWhenYourProxyListensOn443AndRoutesEveryClaimToTheSwitchboard(t *testing.T) {
	t.Parallel()

	machine := &box{
		listening: on443(),
		claimed:   []string{"shop.example.com", "ocel-edge-probe.preview.example.com"},
		answers:   map[string]string{"shop.example.com": "box", "ocel-edge-probe.preview.example.com": "box"},
	}
	inspected, err := (manual.Manual{Box: machine, Port: 8480}).Inspect(context.Background())
	if err != nil {
		t.Fatalf("Inspect() = %v", err)
	}
	checks := verdicts(inspected)
	if len(checks) != 3 {
		t.Fatalf("Inspect() = %+v, want :443 and each claim checked", inspected)
	}
	for subject, check := range checks {
		if check.Verdict != provider.HostPass {
			t.Errorf("%s = %v: %s, want it to pass", subject, check.Verdict, check.Finding)
		}
	}
}

func TestManualFailsWhenNothingListensOn443(t *testing.T) {
	t.Parallel()

	inspected, err := (manual.Manual{Box: &box{}, Port: 8480}).Inspect(context.Background())
	if err != nil {
		t.Fatalf("Inspect() = %v", err)
	}
	check := verdicts(inspected)["tcp 443"]
	if check.Verdict != provider.HostFail || !strings.Contains(check.Finding, "nothing listens on 443") {
		t.Errorf("tcp 443 = %+v, want it failed for nothing listening", check)
	}
}

func TestManualPassesWhenAContainerOfYoursPublishes443WithNothingListeningOnTheHost(t *testing.T) {
	t.Parallel()

	machine := &box{publishing: []string{"traefik"}}
	inspected, err := (manual.Manual{Box: machine, Port: 8480}).Inspect(context.Background())
	if err != nil {
		t.Fatalf("Inspect() = %v", err)
	}
	check := verdicts(inspected)["tcp 443"]
	if check.Verdict != provider.HostPass || !strings.Contains(check.Finding, "traefik") {
		t.Errorf("tcp 443 = %+v, want it passed naming traefik: an engine without its userland proxy publishes through the firewall and nothing listens", check)
	}
}

func TestManualFailsWhenOcelsOwnProxyPublishes443(t *testing.T) {
	t.Parallel()

	machine := &box{listening: on443(), publishing: []string{caddy.Container}}
	inspected, err := (manual.Manual{Box: machine, Port: 8480}).Inspect(context.Background())
	if err != nil {
		t.Fatalf("Inspect() = %v", err)
	}
	check := verdicts(inspected)["tcp 443"]
	if check.Verdict != provider.HostFail || !strings.Contains(check.Finding, caddy.Container) {
		t.Errorf("tcp 443 = %+v, want it failed naming %s", check, caddy.Container)
	}
	if !slices.Contains(machine.asked, "publishing 443") {
		t.Errorf("the box was asked %q, want it asked who publishes 443", machine.asked)
	}
}

func TestManualFailsAClaimYourProxyDoesNotRouteAndSaysWhereToRouteIt(t *testing.T) {
	t.Parallel()

	machine := &box{
		listening: on443(),
		claimed:   []string{"shop.example.com", "api.example.com"},
		answers:   map[string]string{"shop.example.com": "box", "api.example.com": ""},
		unreached: map[string]string{"api.example.com": "api.example.com answered nothing over tls at 127.0.0.1:443"},
	}
	inspected, err := (manual.Manual{Box: machine, Port: 9000}).Inspect(context.Background())
	if err != nil {
		t.Fatalf("Inspect() = %v", err)
	}
	check := verdicts(inspected)["api.example.com"]
	if check.Verdict != provider.HostFail {
		t.Fatalf("api.example.com = %+v, want it failed", check)
	}
	if !strings.Contains(check.Finding, "answered nothing over tls") {
		t.Errorf("finding %q, want what the probe met", check.Finding)
	}
	if want := manual.Route("api.example.com", 9000); check.Fix != want {
		t.Errorf("fix %q, want %q", check.Fix, want)
	}
}

func TestManualFailsAClaimAnsweredByAnotherEdge(t *testing.T) {
	t.Parallel()

	machine := &box{
		listening: on443(),
		claimed:   []string{"shop.example.com"},
		answers:   map[string]string{"shop.example.com": "cloudflare"},
	}
	inspected, err := (manual.Manual{Box: machine, Port: 8480}).Inspect(context.Background())
	if err != nil {
		t.Fatalf("Inspect() = %v", err)
	}
	check := verdicts(inspected)["shop.example.com"]
	if check.Verdict != provider.HostFail || !strings.Contains(check.Finding, "cloudflare") {
		t.Errorf("shop.example.com = %+v, want it failed naming the edge that answered", check)
	}
}

func TestManualFailsTheListenCheckItCouldNotRead(t *testing.T) {
	t.Parallel()

	machine := &box{unread: errors.New("cat: /proc/net/tcp: permission denied")}
	inspected, err := (manual.Manual{Box: machine, Port: 8480}).Inspect(context.Background())
	if err != nil {
		t.Fatalf("Inspect() = %v", err)
	}
	check := verdicts(inspected)["tcp 443"]
	if check.Verdict != provider.HostFail || !strings.Contains(check.Finding, "permission denied") {
		t.Errorf("tcp 443 = %+v, want it failed with what the read met", check)
	}
}

func TestTheRouteNamesTheLoopbackPortAndWhatYourProxyMustKeep(t *testing.T) {
	t.Parallel()

	want := "Route shop.example.com → http://127.0.0.1:8480 (keep Host, set X-Forwarded-Proto)"
	if got := manual.Route("shop.example.com", 8480); got != want {
		t.Errorf("Route() = %q, want %q", got, want)
	}
}
