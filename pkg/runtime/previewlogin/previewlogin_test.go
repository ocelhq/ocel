package previewlogin_test

import (
	"encoding/json"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/pkg/runtime/previewlogin"
)

const (
	gateKey  = "gate-key"
	password = "open sesame"
	host     = "app-abc.preview.example.com"
)

var now = time.Unix(1_800_000_000, 0)

func gateEnv(t *testing.T) string {
	t.Helper()
	encoded, err := json.Marshal(previewgate.Gate{Key: gateKey, PasswordMAC: previewgate.HashPassword(gateKey, password)})
	if err != nil {
		t.Fatal(err)
	}
	return edge.PreviewGateVar + "=" + string(encoded)
}

func loginRequest(body string) previewgate.Request {
	return previewgate.Request{
		Method: http.MethodPost,
		Host:   host,
		Target: previewgate.LoginPath,
		Header: previewgate.Header{"Content-Type": {"application/x-www-form-urlencoded"}},
		Body:   []byte(body),
	}
}

func assertNeverCached(t *testing.T, answer previewgate.Response) {
	t.Helper()
	if got := answer.Header.Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("Cache-Control = %q, want private, no-store", got)
	}
	if got := answer.Header.Get("X-Robots-Tag"); got != "noindex" {
		t.Errorf("X-Robots-Tag = %q, want noindex", got)
	}
}

func TestTheLoginWithThePasswordRedirectsWithASessionCookieForTheHost(t *testing.T) {
	login, _ := previewlogin.FromEnv([]string{gateEnv(t)})

	answer := login.Answer(loginRequest("password=open+sesame&next=%2Fdocs"), now)

	if answer.Status != http.StatusSeeOther || answer.Header.Get("Location") != "/docs" {
		t.Fatalf("Answer = %d to %q, want 303 to /docs", answer.Status, answer.Header.Get("Location"))
	}
	if cookie := answer.Header.Get("Set-Cookie"); !strings.HasPrefix(cookie, previewgate.CookieName+"=") {
		t.Errorf("Set-Cookie = %q, want the %s session", cookie, previewgate.CookieName)
	}
	assertNeverCached(t, answer)
}

func TestTheLoginWithAWrongPasswordIsRefusedWithTheForm(t *testing.T) {
	login, _ := previewlogin.FromEnv([]string{gateEnv(t)})

	answer := login.Answer(loginRequest("password=guess"), now)

	if answer.Status != http.StatusUnauthorized || answer.Header.Get("Set-Cookie") != "" {
		t.Errorf("Answer = %d setting %q, want 401 and no cookie", answer.Status, answer.Header.Get("Set-Cookie"))
	}
	assertNeverCached(t, answer)
}

func TestTheLoginIsRefusedWhenNoGateKeysReachedTheOrigin(t *testing.T) {
	login, _ := previewlogin.FromEnv(nil)

	answer := login.Answer(loginRequest("password=open+sesame"), now)

	if answer.Status != http.StatusForbidden || answer.Header.Get("Set-Cookie") != "" {
		t.Errorf("Answer = %d setting %q, want 403 and no cookie: there are no keys to check the password against", answer.Status, answer.Header.Get("Set-Cookie"))
	}
	assertNeverCached(t, answer)
}

func TestTheLoginIsRefusedWhenTheGateKeysDoNotParse(t *testing.T) {
	for _, value := range []string{"{", `{"key":""}`, `[]`} {
		login, _ := previewlogin.FromEnv([]string{edge.PreviewGateVar + "=" + value})

		answer := login.Answer(loginRequest("password=open+sesame"), now)

		if answer.Status != http.StatusServiceUnavailable {
			t.Errorf("Answer with %s=%s = %d, want 503", edge.PreviewGateVar, value, answer.Status)
		}
		assertNeverCached(t, answer)
	}
}

func TestReadingTheEnvironmentKeepsTheGateKeysFromTheApp(t *testing.T) {
	_, kept := previewlogin.FromEnv([]string{"PATH=/bin", gateEnv(t), "HOME=/root"})

	if want := []string{"PATH=/bin", "HOME=/root"}; !slices.Equal(kept, want) {
		t.Errorf("kept = %q, want %q", kept, want)
	}
}

func TestOnlyAPostToTheLoginPathIsTheLogin(t *testing.T) {
	for _, tc := range []struct {
		method, path string
		want         bool
	}{
		{http.MethodPost, previewgate.LoginPath, true},
		{http.MethodGet, previewgate.LoginPath, false},
		{http.MethodPost, previewgate.LoginPath + "/", false},
		{http.MethodPost, "/", false},
	} {
		if got := previewlogin.IsLogin(tc.method, tc.path); got != tc.want {
			t.Errorf("IsLogin(%s, %s) = %t, want %t", tc.method, tc.path, got, tc.want)
		}
	}
}

func TestReadingTheBodyStopsPastTheLoginCap(t *testing.T) {
	read, err := previewlogin.ReadBody(strings.NewReader(strings.Repeat("a", 1<<20)))
	if err != nil {
		t.Fatal(err)
	}

	if got, want := len(read), previewgate.LoginBodyMaxBytes+1; got != want {
		t.Errorf("read %d bytes, want %d: no more than one byte past the cap", got, want)
	}
}

func TestAnOversizedLoginIsRefusedWithoutCheckingThePassword(t *testing.T) {
	login, _ := previewlogin.FromEnv([]string{gateEnv(t)})
	body := "password=open+sesame&pad=" + strings.Repeat("a", previewgate.LoginBodyMaxBytes)
	read, err := previewlogin.ReadBody(strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	req := loginRequest("")
	req.Body = read

	if answer := login.Answer(req, now); answer.Status != http.StatusRequestEntityTooLarge {
		t.Errorf("Answer = %d, want 413", answer.Status)
	}
}
