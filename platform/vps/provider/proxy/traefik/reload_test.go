package traefik_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/vps/provider/proxy"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

func claiming(hostnames ...string) proxy.Spec { return proxy.Spec{Hostnames: hostnames} }

func unreloaded(t *testing.T, err error, mentions ...string) {
	t.Helper()
	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
		t.Fatalf("Reload() = %v, want a not-ready refusal", err)
	}
	for _, mention := range mentions {
		if !strings.Contains(err.Error(), mention) {
			t.Errorf("Reload() = %v, want it to name %s", err, mention)
		}
	}
}

func TestAReloadWaitsUntilYourTraefikRoutesEveryHostnameToTheSwitchboard(t *testing.T) {
	t.Parallel()

	machine := &box{
		spec:   claiming("shop.example.com", "api.example.com"),
		routes: map[string][]string{"shop.example.com": {"", "", "switchboard"}, "api.example.com": {"switchboard"}},
	}
	if err := coolifys(machine).Reload(context.Background(), proxy.Spec{}); err != nil {
		t.Fatalf("Reload() = %v, want it to wait out Traefik's reload", err)
	}
	if len(machine.paused) != 2 {
		t.Errorf("Reload() paused %v, want twice: once for each probe shop.example.com was not routed yet", machine.paused)
	}
	if asked := strings.Count(strings.Join(machine.asked, "\n"), "routed api.example.com"); asked != 1 {
		t.Errorf("api.example.com was probed %d times, want once: it was routed at the first ask", asked)
	}
}

func TestAReloadOfABoxThatClaimsNothingAsksNothing(t *testing.T) {
	t.Parallel()

	machine := &box{}
	if err := coolifys(machine).Reload(context.Background(), proxy.Spec{}); err != nil {
		t.Fatalf("Reload() = %v", err)
	}
	if slices.ContainsFunc(machine.asked, func(asked string) bool { return strings.HasPrefix(asked, "routed") }) || len(machine.paused) > 0 {
		t.Errorf("Reload() asked %q and paused %v, want nothing: there is no route to wait on", machine.asked, machine.paused)
	}
}

func TestAReloadYourTraefikNeverTakesUpSaysWhatTheProbeMetWithinTheWait(t *testing.T) {
	t.Parallel()

	machine := &box{
		spec:     claiming("shop.example.com"),
		failures: map[string]string{"shop.example.com": "shop.example.com answered at 127.0.0.1:443 as \"\", and ocel-switchboard never did"},
		beside:   besideIn(t, "coolify"),
	}
	err := coolifys(machine).Reload(context.Background(), proxy.Spec{})
	unreloaded(t, err, "shop.example.com", "ocel-switchboard never did", "30s")
	if strings.Contains(err.Error(), "does not parse") || strings.Contains(err.Error(), "Caddy") {
		t.Errorf("Reload() = %v, want no file or proxy blamed on a box where neither is at fault", err)
	}
}

func TestAReloadStuckBehindAFileOfYoursThatDoesNotParseNamesIt(t *testing.T) {
	t.Parallel()

	for name, content := range map[string]string{
		"broken.yml":    "http:\n  routers:\n    web:\n      rule: Host(`a.example.com`)\n     service: web\n",
		"misspelt.yaml": "http:\n  routrs:\n    web:\n      rule: Host(`a.example.com`)\n",
		"broken.toml":   "[http.routers.web\nrule = 1\n",
		"template.yml":  "http:\n  routers:\n    {{ if }}\n",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()

			machine := &box{
				spec:   claiming("shop.example.com"),
				beside: append(besideIn(t, "dokploy"), switchboard.SiblingFile{Name: name, Content: []byte(content)}),
			}
			err := dokploys(machine).Reload(context.Background(), proxy.Spec{})
			unreloaded(t, err, dokployDynamic+"/"+name, "does not parse")
			for _, theirs := range []string{"dapp-lekbai.yml", "dokploy.yml", "middlewares.yml"} {
				if strings.Contains(err.Error(), theirs) {
					t.Errorf("Reload() = %v, want %s, which parses, left unblamed", err, theirs)
				}
			}
		})
	}
}

func TestAReloadOnABoxWhereCoolifyNowRunsCaddySaysSoAndWhatToWrite(t *testing.T) {
	t.Parallel()

	machine := &box{
		spec:    claiming("shop.example.com"),
		beside:  besideIn(t, "coolify"),
		coolify: "lucaslorentz/caddy-docker-proxy:2.9-alpine\n",
	}
	err := coolifys(machine).Reload(context.Background(), proxy.Spec{})
	unreloaded(t, err, "Coolify", "Caddy", `{ "caddy": { "preset": "coolify" } }`)
}

func TestTheReloadWaitIsLongEnoughForTraefiksThrottle(t *testing.T) {
	t.Parallel()

	machine := &box{spec: claiming("shop.example.com")}
	_ = coolifys(machine).Reload(context.Background(), proxy.Spec{})
	var waited float64
	for _, pause := range machine.paused {
		waited += pause.Seconds()
	}
	if throttle := 2 * time.Second; waited < 2*throttle.Seconds() {
		t.Errorf("Reload() waited %.0fs in all, want well past Traefik's %s throttle on a second change", waited, throttle)
	}
}

func TestAReloadWaitsOnlyOnTheHostnamesTheWriteAddsAndLetsAnUnrelatedFailingOneBe(t *testing.T) {
	t.Parallel()

	machine := &box{
		spec:     claiming("api.example.com", "shop.example.com"),
		routes:   map[string][]string{"shop.example.com": {"switchboard"}},
		failures: map[string]string{"api.example.com": "api.example.com answered at 127.0.0.1:443 as \"\", and ocel-switchboard never did"},
	}
	if err := coolifys(machine).Reload(context.Background(), claiming("api.example.com")); err != nil {
		t.Fatalf("Reload() = %v, want the write taken up once shop.example.com, the one hostname it adds, is routed", err)
	}
	if slices.Contains(machine.asked, "routed api.example.com") || len(machine.paused) > 0 {
		t.Errorf("Reload() asked %q and paused %v, want api.example.com left alone: its router is what it was before the write", machine.asked, machine.paused)
	}
}

func TestAReloadWaitsOnAHostnameWhoseRouterTheWriteChanges(t *testing.T) {
	t.Parallel()

	machine := &box{
		spec:   proxy.Spec{Hostnames: []string{"ocel-edge-probe.preview.example.com", "pr-12--web.preview.example.com"}, PreviewBase: "preview.example.com"},
		routes: map[string][]string{"ocel-edge-probe.preview.example.com": {"switchboard"}, "pr-12--web.preview.example.com": {"", "switchboard"}},
	}
	front := coolifys(machine)
	front.PreviewResolver = "cloudflare"
	if err := front.Reload(context.Background(), claiming("pr-12--web.preview.example.com")); err != nil {
		t.Fatalf("Reload() = %v", err)
	}
	if asked := strings.Count(strings.Join(machine.asked, "\n"), "routed pr-12--web.preview.example.com"); asked != 2 {
		t.Errorf("pr-12--web.preview.example.com was probed %d times, want twice: the preview base moved it onto the wildcard certificate, so its router changed", asked)
	}
}
