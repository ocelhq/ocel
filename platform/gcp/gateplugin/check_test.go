package gateplugin_test

import (
	"encoding/base64"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/gcp/gateplugin"
)

func TestThePluginAnswersEveryConformanceVector(t *testing.T) {
	t.Parallel()

	edgeconformance.RunGateWith(t, func(t *testing.T, held gate.Gate, req gate.Request, now time.Time) gate.Verdict {
		config, err := gateplugin.ParseConfig(gateplugin.EncodeConfig(
			map[string]string{req.Host: "shop"},
			map[string]gate.Gate{"shop": held},
		))
		if err != nil {
			t.Fatalf("ParseConfig = %v", err)
		}
		return config.Check(requestAsEnvoySendsIt(req), now)
	})
}

func requestAsEnvoySendsIt(req gate.Request) gate.Request {
	pairs := [][2]string{{":method", req.Method}, {":path", req.Target}, {":authority", req.Host}}
	for name, values := range req.Header {
		pairs = append(pairs, [2]string{strings.ToLower(name), strings.Join(values, ", ")})
	}
	return gateplugin.ReadRequest(pairs)
}

func TestARequestForAHostNoGateNamesPassesUnchanged(t *testing.T) {
	t.Parallel()

	config, err := gateplugin.ParseConfig(gateplugin.EncodeConfig(
		map[string]string{"gated.example.dev": "shop"},
		map[string]gate.Gate{"shop": {Key: "k"}},
	))
	if err != nil {
		t.Fatal(err)
	}
	req := gateplugin.ReadRequest([][2]string{
		{":method", "GET"}, {":path", "/"}, {":authority", "open.example.dev"}, {"authorization", "Basic eDp5"},
	})

	verdict := config.Check(req, time.Unix(1700000000, 0))

	if verdict.Response != nil {
		t.Fatalf("Check answered %d for a host nothing gates", verdict.Response.Status)
	}
	if remove, replace := gateplugin.ListHeaderEdits(req.Header, verdict.Forward); len(remove) != 0 || len(replace) != 0 {
		t.Errorf("Edits = remove %q replace %q, want the request forwarded as it came", remove, replace)
	}
}

func TestAnUncredentialedRequestForAProtectedHostIsAnsweredByThePluginBeforeAnyCacheAndNeverStored(t *testing.T) {
	t.Parallel()

	config, err := gateplugin.ParseConfig(gateplugin.EncodeConfig(
		map[string]string{"shop.example.com": "shop", "pr-7-shop.preview.example.com": "shop"},
		map[string]gate.Gate{"shop": {Key: "k", PasswordMAC: "m"}},
	))
	if err != nil {
		t.Fatal(err)
	}
	for _, host := range []string{"shop.example.com", "pr-7-shop.preview.example.com"} {
		req := gateplugin.ReadRequest([][2]string{{":method", "GET"}, {":path", "/cached-page"}, {":authority", host}})

		verdict := config.Check(req, time.Unix(1700000000, 0))

		if verdict.Response == nil || verdict.Response.Status != 401 {
			t.Fatalf("Check(%s) = %+v, want the plugin to answer 401 itself: a request it forwards reaches Cloud CDN, which serves whatever it has cached for the URL", host, verdict)
		}
		if got := verdict.Response.Header.Get("Cache-Control"); got != gate.CacheControl {
			t.Errorf("the refusal for %s carries Cache-Control %q, want %q so nothing stores it", host, got, gate.CacheControl)
		}
		if got := verdict.Response.Header.Get("X-Robots-Tag"); got != gate.RobotsTag {
			t.Errorf("the refusal for %s carries X-Robots-Tag %q, want %q", host, got, gate.RobotsTag)
		}
	}
}

func TestTheLoginPostIsAnsweredByThePluginWithTheSessionCookie(t *testing.T) {
	t.Parallel()

	held := gate.Gate{Key: "k"}
	held.PasswordMAC = gate.HashPassword(held.Key, "pw")
	config, err := gateplugin.ParseConfig(gateplugin.EncodeConfig(
		map[string]string{"gated.example.dev": "shop"},
		map[string]gate.Gate{"shop": held},
	))
	if err != nil {
		t.Fatal(err)
	}
	req := gateplugin.ReadRequest([][2]string{
		{":method", "POST"}, {":path", gate.LoginPath}, {":authority", "gated.example.dev"},
		{"authorization", "Basic " + base64.StdEncoding.EncodeToString([]byte(":pw"))},
	})

	verdict := config.Check(req, time.Unix(1700000000, 0))

	if verdict.Response == nil || verdict.Response.Status != 204 {
		t.Fatalf("Check = %+v, want the plugin to answer 204 itself: the password never reaches the service", verdict)
	}
	if !strings.HasPrefix(verdict.Response.Header.Get("Set-Cookie"), gate.CookieName+"=") {
		t.Errorf("Set-Cookie = %q, want the session cookie", verdict.Response.Header.Get("Set-Cookie"))
	}
}

func TestASessionCookieOnASecondCookieLineIsAdmittedAndRemovedBeforeForwarding(t *testing.T) {
	t.Parallel()

	held := gate.Gate{Key: "k", PasswordMAC: "m"}
	now := time.Unix(1700000000, 0)
	session := sessionCookie(held, "gated.example.dev", now)
	config, err := gateplugin.ParseConfig(gateplugin.EncodeConfig(
		map[string]string{"gated.example.dev": "shop"},
		map[string]gate.Gate{"shop": held},
	))
	if err != nil {
		t.Fatal(err)
	}
	req := gateplugin.ReadRequest([][2]string{
		{":method", "GET"}, {":path", "/"}, {":authority", "gated.example.dev"},
		{"cookie", "theme=dark, " + session},
	})

	verdict := config.Check(req, now)

	if verdict.Response != nil {
		t.Fatalf("Check answered %d, want the session on the second Cookie line admitted", verdict.Response.Status)
	}
	_, replace := gateplugin.ListHeaderEdits(req.Header, verdict.Forward)
	if got := replace["cookie"]; got != "theme=dark" {
		t.Errorf("the forwarded cookie = %q, want the app's own cookie alone", got)
	}
}

func TestACookieTheGateDoesNotReadIsForwardedByteForByte(t *testing.T) {
	t.Parallel()

	held := gate.Gate{Key: "k", PasswordMAC: "m", BypassSecrets: []string{"s"}}
	config, err := gateplugin.ParseConfig(gateplugin.EncodeConfig(
		map[string]string{"gated.example.dev": "shop"},
		map[string]gate.Gate{"shop": held},
	))
	if err != nil {
		t.Fatal(err)
	}
	req := gateplugin.ReadRequest([][2]string{
		{":method", "GET"}, {":path", "/"}, {":authority", "gated.example.dev"},
		{"x-ocel-bypass", "s"}, {"cookie", "a=1, b=2; c=3"},
	})

	verdict := config.Check(req, time.Unix(1700000000, 0))
	remove, replace := gateplugin.ListHeaderEdits(req.Header, verdict.Forward)

	if !slices.Equal(remove, []string{"x-ocel-bypass"}) {
		t.Errorf("remove = %q, want only the bypass header", remove)
	}
	if _, touched := replace["cookie"]; touched {
		t.Errorf("replace = %q, want the cookie the app sent left alone", replace)
	}
}

func sessionCookie(held gate.Gate, host string, now time.Time) string {
	login := gate.Request{
		Method: "POST", Host: host, Target: gate.LoginPath,
		Header: gate.Header{"Authorization": {"Basic " + base64.StdEncoding.EncodeToString([]byte(":pw"))}},
	}
	held.PasswordMAC = gate.HashPassword(held.Key, "pw")
	cookie := held.Check(login, now).Response.Header.Get("Set-Cookie")
	pair, _, _ := strings.Cut(cookie, ";")
	return pair
}

func TestEveryResponseThePluginAnswersItselfNamesTheLoadBalancerAsTheRouter(t *testing.T) {
	t.Parallel()

	held := gate.Gate{Key: "k", BypassSecrets: []string{"bypass"}}
	held.PasswordMAC = gate.HashPassword(held.Key, "pw")
	config, err := gateplugin.ParseConfig(gateplugin.EncodeConfig(
		map[string]string{"gated.example.dev": "shop"},
		map[string]gate.Gate{"shop": held},
	))
	if err != nil {
		t.Fatal(err)
	}
	asked := map[int][][2]string{
		401: {{":method", "GET"}, {":path", edge.LivenessProbePath}},
		204: {{":method", "POST"}, {":path", gate.LoginPath}, {"authorization", "Basic " + base64.StdEncoding.EncodeToString([]byte(":pw"))}},
		303: {{":method", "GET"}, {":path", "/"}, {"x-ocel-bypass", "bypass"}, {"x-ocel-set-bypass-cookie", "true"}},
	}
	for status, pairs := range asked {
		req := gateplugin.ReadRequest(append(pairs, [2]string{":authority", "gated.example.dev"}))

		verdict := config.Check(req, time.Unix(1700000000, 0))

		if verdict.Response == nil || verdict.Response.Status != status {
			t.Fatalf("Check(%v) = %+v, want the plugin to answer %d itself", pairs, verdict, status)
		}
		if got := verdict.Response.Header.Get(router.HeaderRouter); got != gateplugin.RouterKind {
			t.Errorf("the %d the plugin answers carries %s %q, want %q: an edge extension answers before the load balancer evaluates the URL map whose header action names the router, and without it the deploy's readiness probe reads a protected hostname as unserved", status, router.HeaderRouter, got, gateplugin.RouterKind)
		}
	}
}

func TestThePluginNamesTheRouterUnderTheHeaderTheProbeReads(t *testing.T) {
	t.Parallel()

	if gateplugin.RouterHeader != router.HeaderRouter {
		t.Errorf("the plugin names the router under %q, want %q: the readiness probe reads that header and nothing else", gateplugin.RouterHeader, router.HeaderRouter)
	}
}
