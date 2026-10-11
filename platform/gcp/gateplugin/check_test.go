package gateplugin_test

import (
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/platform/gcp/gateplugin"
)

func TestThePluginAnswersEveryConformanceVectorThatNeedsNoBody(t *testing.T) {
	t.Parallel()

	edgeconformance.RunPreviewGateWith(t, func(gate previewgate.Gate, req previewgate.Request, now time.Time) previewgate.Verdict {
		config, err := gateplugin.ParseConfig(gateplugin.EncodeConfig(
			map[string]string{req.Host: "shop"},
			map[string]previewgate.Gate{"shop": gate},
		))
		if err != nil {
			t.Fatalf("ParseConfig = %v", err)
		}
		return config.Check(requestAsEnvoySendsIt(req), now)
	}, true)
}

func requestAsEnvoySendsIt(req previewgate.Request) previewgate.Request {
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
		map[string]previewgate.Gate{"shop": {Key: "k"}},
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

func TestTheLoginPostPassesToTheOriginBecauseNoBodyReachesThePlugin(t *testing.T) {
	t.Parallel()

	config, err := gateplugin.ParseConfig(gateplugin.EncodeConfig(
		map[string]string{"gated.example.dev": "shop"},
		map[string]previewgate.Gate{"shop": {Key: "k"}},
	))
	if err != nil {
		t.Fatal(err)
	}
	req := gateplugin.ReadRequest([][2]string{
		{":method", "POST"}, {":path", previewgate.LoginPath}, {":authority", "gated.example.dev"},
		{"content-type", "application/x-www-form-urlencoded"},
	})

	verdict := config.Check(req, time.Unix(1700000000, 0))

	if verdict.Response != nil {
		t.Fatalf("Check answered %d, want the login forwarded: the origin holds the keys and reads the body", verdict.Response.Status)
	}
}

func TestASessionCookieOnASecondCookieLineIsAdmittedAndRemovedBeforeForwarding(t *testing.T) {
	t.Parallel()

	gate := previewgate.Gate{Key: "k", PasswordMAC: "m"}
	now := time.Unix(1700000000, 0)
	session := sessionCookie(gate, "gated.example.dev", now)
	config, err := gateplugin.ParseConfig(gateplugin.EncodeConfig(
		map[string]string{"gated.example.dev": "shop"},
		map[string]previewgate.Gate{"shop": gate},
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

	gate := previewgate.Gate{Key: "k", PasswordMAC: "m", BypassSecrets: []string{"s"}}
	config, err := gateplugin.ParseConfig(gateplugin.EncodeConfig(
		map[string]string{"gated.example.dev": "shop"},
		map[string]previewgate.Gate{"shop": gate},
	))
	if err != nil {
		t.Fatal(err)
	}
	req := gateplugin.ReadRequest([][2]string{
		{":method", "GET"}, {":path", "/"}, {":authority", "gated.example.dev"},
		{"x-ocel-preview-bypass", "s"}, {"cookie", "a=1, b=2; c=3"},
	})

	verdict := config.Check(req, time.Unix(1700000000, 0))
	remove, replace := gateplugin.ListHeaderEdits(req.Header, verdict.Forward)

	if !slices.Equal(remove, []string{"x-ocel-preview-bypass"}) {
		t.Errorf("remove = %q, want only the bypass header", remove)
	}
	if _, touched := replace["cookie"]; touched {
		t.Errorf("replace = %q, want the cookie the app sent left alone", replace)
	}
}

func sessionCookie(gate previewgate.Gate, host string, now time.Time) string {
	login := previewgate.Request{
		Method: "POST", Host: host, Target: previewgate.LoginPath,
		Header: previewgate.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
		Body:   []byte("password=pw"),
	}
	gate.PasswordMAC = previewgate.HashPassword(gate.Key, "pw")
	cookie := gate.Check(login, now).Response.Header.Get("Set-Cookie")
	pair, _, _ := strings.Cut(cookie, ";")
	return pair
}
