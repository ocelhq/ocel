package main

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
)

const (
	loginGateKey      = "gate-key"
	loginPassword     = "open sesame"
	loginOriginSecret = "0123456789abcdef0123456789abcdef"
)

type loginEvent struct {
	body       string
	base64     bool
	withSecret bool
}

func (e loginEvent) payload(t *testing.T) []byte {
	t.Helper()
	headers := map[string]string{
		"host":             "abc.lambda-url.eu-west-1.on.aws",
		"x-forwarded-host": "app-abc.preview.example.com",
		"content-type":     "application/x-www-form-urlencoded",
	}
	if e.withSecret {
		headers[originguard.OriginSecretHeader] = loginOriginSecret
	}
	body := e.body
	if e.base64 {
		body = base64.StdEncoding.EncodeToString([]byte(body))
	}
	encoded, err := json.Marshal(map[string]any{
		"version":         "2.0",
		"rawPath":         previewgate.LoginPath,
		"requestContext":  map[string]any{"http": map[string]string{"method": http.MethodPost}},
		"headers":         headers,
		"body":            body,
		"isBase64Encoded": e.base64,
	})
	if err != nil {
		t.Fatal(err)
	}
	return encoded
}

func gatedFront(t *testing.T) loginFront {
	t.Helper()
	gate, err := json.Marshal(previewgate.Gate{Key: loginGateKey, PasswordMAC: previewgate.HashPassword(loginGateKey, loginPassword)})
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv(edge.PreviewGateVar, string(gate))
	t.Setenv(originguard.OriginSecretVar, loginOriginSecret)
	return readLoginFront()
}

func loginAnswered(t *testing.T, front loginFront, event loginEvent) (prelude, []byte, bool) {
	t.Helper()
	reached := false
	app := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		reached = true
		_, _ = io.Copy(io.Discard, r.Body)
		io.WriteString(w, "the app")
	}))
	t.Cleanup(app.Close)
	rt, captured := fakeRuntime(t, event.payload(t))
	m := &nodeChild{upstream: upstream{port: portOf(t, app), client: newLoopbackClient()}}

	if err := handleInvocation(t.Context(), rt, m, front); err != nil {
		t.Fatalf("handleInvocation: %v", err)
	}
	p, body := splitPrelude(t, captured.body)
	return p, body, reached
}

func TestTheRootFunctionAnswersTheLoginItselfAndSetsTheSession(t *testing.T) {
	p, _, reached := loginAnswered(t, gatedFront(t), loginEvent{body: "password=open+sesame&next=%2Fdocs", withSecret: true})

	if reached {
		t.Error("the login reached the app, carrying the password")
	}
	if p.StatusCode != http.StatusSeeOther || p.Headers["Location"] != "/docs" {
		t.Errorf("answer = %d to %q, want 303 to /docs", p.StatusCode, p.Headers["Location"])
	}
	if len(p.Cookies) != 1 || !strings.HasPrefix(p.Cookies[0], previewgate.CookieName+"=") {
		t.Errorf("cookies = %q, want the %s session", p.Cookies, previewgate.CookieName)
	}
	if p.Headers["Cache-Control"] != "private, no-store" || p.Headers["X-Robots-Tag"] != "noindex" {
		t.Errorf("headers = %v, want private, no-store and noindex", p.Headers)
	}
}

func TestTheRootFunctionReadsABase64LoginBody(t *testing.T) {
	p, _, _ := loginAnswered(t, gatedFront(t), loginEvent{body: "password=open+sesame", base64: true, withSecret: true})

	if p.StatusCode != http.StatusSeeOther {
		t.Errorf("answer = %d, want 303", p.StatusCode)
	}
}

func TestTheRootFunctionRefusesALoginBodyPastTheCap(t *testing.T) {
	for _, encoded := range []bool{false, true} {
		body := "password=open+sesame&pad=" + strings.Repeat("a", previewgate.LoginBodyMaxBytes)

		p, _, reached := loginAnswered(t, gatedFront(t), loginEvent{body: body, base64: encoded, withSecret: true})

		if p.StatusCode != http.StatusRequestEntityTooLarge || reached {
			t.Errorf("base64 %t: answer = %d, reached the app = %t, want 413 and never the app", encoded, p.StatusCode, reached)
		}
	}
}

func TestTheRootFunctionRefusesTheLoginWhenItHoldsNoGateKeys(t *testing.T) {
	t.Setenv(originguard.OriginSecretVar, loginOriginSecret)

	p, _, reached := loginAnswered(t, readLoginFront(), loginEvent{body: "password=open+sesame", withSecret: true})

	if p.StatusCode != http.StatusForbidden || len(p.Cookies) != 0 || reached {
		t.Errorf("answer = %d with cookies %q, reached the app = %t, want 403, no cookie and never the app", p.StatusCode, p.Cookies, reached)
	}
}

func TestTheRootFunctionRefusesALoginThatDidNotComeThroughTheEdge(t *testing.T) {
	p, _, reached := loginAnswered(t, gatedFront(t), loginEvent{body: "password=open+sesame"})

	if p.StatusCode != http.StatusForbidden || len(p.Cookies) != 0 || reached {
		t.Errorf("answer = %d with cookies %q, reached the app = %t, want 403 without the origin secret", p.StatusCode, p.Cookies, reached)
	}
}

func TestTheGateKeysNeverReachTheAppsEnvironment(t *testing.T) {
	gatedFront(t)

	if value, set := os.LookupEnv(edge.PreviewGateVar); set {
		t.Errorf("%s = %q after the runtime read it, want it gone before the app starts", edge.PreviewGateVar, value)
	}
}
