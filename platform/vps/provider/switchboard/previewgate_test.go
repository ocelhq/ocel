package switchboard_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge/previewgate"
	"github.com/ocelhq/ocel/platform/vps/provider/switchboard"
)

const (
	previewHost = "abc12.preview.example.com"
	openHost    = "plain.example.com"
	previewPass = "hunter2"
	previewSalt = "bypass-secret-1"
)

var shopPreviewGate = previewgate.Gate{
	Key:           "gate-key",
	PasswordMAC:   previewgate.HashPassword("gate-key", previewPass),
	BypassSecrets: []string{previewSalt},
}

func previewGatedRouting(t *testing.T, previewGate previewgate.Gate, previewUpstream, openUpstream string) []byte {
	t.Helper()
	type heldPreviewGate struct {
		Owner       string           `json:"owner"`
		PreviewGate previewgate.Gate `json:"previewGate"`
	}
	document := map[string]any{
		"grace": "30s",
		"claims": []map[string]string{
			{"owner": "ocel--shop--preview", "hostname": previewHost, "pointer": "pr-1", "app": "web"},
			{"owner": "ocel--blog--production", "hostname": openHost, "pointer": "@production", "app": "web"},
		},
		"routes": []map[string]string{
			{"owner": "ocel--shop--preview", "pointer": "pr-1", "app": "web", "upstream": previewUpstream},
			{"owner": "ocel--blog--production", "pointer": "@production", "app": "web", "upstream": openUpstream},
		},
		"previewGates": []heldPreviewGate{{Owner: "ocel--shop--preview", PreviewGate: previewGate}},
	}
	written, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}
	return written
}

type seenByApp struct {
	authorization string
	cookie        string
	bypass        string
	reached       int
}

func appBehindPreviewGate(t *testing.T, header http.Header) (string, *seenByApp) {
	t.Helper()
	seen := &seenByApp{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.reached++
		seen.authorization = r.Header.Get("Authorization")
		seen.cookie = r.Header.Get("Cookie")
		seen.bypass = r.Header.Get(previewgate.HeaderBypass)
		for name, values := range header {
			w.Header()[name] = values
		}
		_, _ = io.WriteString(w, "the page")
	}))
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://"), seen
}

func previewGatedBoard(t *testing.T, appHeader http.Header) (string, *seenByApp) {
	t.Helper()
	app, seen := appBehindPreviewGate(t, appHeader)
	_, at := served(t, previewGatedRouting(t, shopPreviewGate, app, backend(t, "plain")))
	return at, seen
}

func basic(password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("anyone:"+password))
}

func postLogin(t *testing.T, at, host, body string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "http://"+at+previewgate.LoginPath, strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	request.Host = host
	request.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	response, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	return response
}

func TestAPreviewHostRefusesARequestWithoutCredentialsAndNeverReachesTheApp(t *testing.T) {
	t.Parallel()
	at, seen := previewGatedBoard(t, nil)

	said := ask(t, boardClient, at, previewHost, "/cart")

	if said.status != http.StatusUnauthorized {
		t.Fatalf("a request without credentials answered %d, want 401", said.status)
	}
	if got := said.header.Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("the refusal carries Cache-Control %q, want %q so a proxy in front never stores it", got, "private, no-store")
	}
	if got := said.header.Get("X-Robots-Tag"); got != "noindex" {
		t.Errorf("the refusal carries X-Robots-Tag %q, want noindex", got)
	}
	if !strings.Contains(said.body, `action="`+previewgate.LoginPath+`"`) || strings.Contains(said.body, "the page") {
		t.Errorf("the refusal body = %q, want the login form and none of the page", said.body)
	}
	if seen.reached != 0 {
		t.Errorf("the app was reached %d times, want none", seen.reached)
	}
}

func TestAPreviewHostServesTheAppToEachCredentialAndStripsWhatThePreviewGateConsumed(t *testing.T) {
	t.Parallel()
	for name, credential := range map[string][2]string{
		"Basic credentials": {"Authorization", basic(previewPass)},
		"the bypass header": {previewgate.HeaderBypass, previewSalt},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			at, seen := previewGatedBoard(t, nil)

			said := ask(t, boardClient, at, previewHost, "/cart", credential[0], credential[1])

			if said.status != http.StatusOK || said.body != "the page" {
				t.Fatalf("answered %d %q, want the app's page", said.status, said.body)
			}
			if got := said.header.Get("X-Robots-Tag"); got != "noindex" {
				t.Errorf("X-Robots-Tag = %q, want noindex on every preview response", got)
			}
			if seen.authorization != "" || seen.bypass != "" {
				t.Errorf("the app saw Authorization %q and bypass %q, want the credentials the preview gate consumed stripped", seen.authorization, seen.bypass)
			}
		})
	}
}

func TestAPreviewHostRefusesWrongCredentials(t *testing.T) {
	t.Parallel()
	at, seen := previewGatedBoard(t, nil)

	for _, wrong := range [][2]string{
		{"Authorization", basic("wrong")},
		{previewgate.HeaderBypass, "not-the-secret"},
		{"Cookie", previewgate.CookieName + "=9999999999." + strings.Repeat("a", 64)},
	} {
		if said := ask(t, boardClient, at, previewHost, "/", wrong[0], wrong[1]); said.status != http.StatusUnauthorized {
			t.Errorf("%s answered %d, want 401", wrong[0], said.status)
		}
	}
	if seen.reached != 0 {
		t.Errorf("the app was reached %d times, want none", seen.reached)
	}
}

func TestABearerTokenPassesThroughToTheAppButDoesNotOpenThePreviewGate(t *testing.T) {
	t.Parallel()
	at, seen := previewGatedBoard(t, nil)

	if said := ask(t, boardClient, at, previewHost, "/api", "Authorization", "Bearer token"); said.status != http.StatusUnauthorized {
		t.Fatalf("a bearer token alone answered %d, want 401", said.status)
	}
	said := ask(t, boardClient, at, previewHost, "/api", "Authorization", "Bearer token", previewgate.HeaderBypass, previewSalt)
	if said.status != http.StatusOK || seen.authorization != "Bearer token" {
		t.Errorf("answered %d with the app seeing Authorization %q, want 200 and the bearer token untouched", said.status, seen.authorization)
	}
}

func TestALoginPostAnswersTheCookieThatOpensThePreviewAndTheAppNeverSeesIt(t *testing.T) {
	t.Parallel()
	at, seen := previewGatedBoard(t, nil)

	response := postLogin(t, at, previewHost, "password="+previewPass+"&next=%2Fcart%3Fa%3D1")

	if response.StatusCode != http.StatusSeeOther || response.Header.Get("Location") != "/cart?a=1" {
		t.Fatalf("the login answered %d to %q, want 303 back to /cart?a=1", response.StatusCode, response.Header.Get("Location"))
	}
	if got := response.Header.Get("Cache-Control"); got != "private, no-store" {
		t.Errorf("the login answer carries Cache-Control %q, want %q", got, "private, no-store")
	}
	cookie, _, _ := strings.Cut(response.Header.Get("Set-Cookie"), ";")
	if !strings.HasPrefix(cookie, previewgate.CookieName+"=") {
		t.Fatalf("Set-Cookie = %q, want the session cookie", response.Header.Get("Set-Cookie"))
	}
	said := ask(t, boardClient, at, previewHost, "/cart", "Cookie", cookie+"; theme=dark")
	if said.status != http.StatusOK {
		t.Fatalf("the cookie the login set answered %d, want 200", said.status)
	}
	if seen.cookie != "theme=dark" {
		t.Errorf("the app saw Cookie %q, want only the cookies the preview gate did not consume", seen.cookie)
	}
}

func TestALoginPostWithAWrongPasswordOrAnOversizedBodyOpensNothing(t *testing.T) {
	t.Parallel()
	at, seen := previewGatedBoard(t, nil)

	for body, want := range map[string]int{
		"password=wrong":                        http.StatusUnauthorized,
		"password=" + strings.Repeat("a", 2048): http.StatusRequestEntityTooLarge,
	} {
		response := postLogin(t, at, previewHost, body)
		if response.StatusCode != want || response.Header.Get("Set-Cookie") != "" {
			t.Errorf("a login of %d bytes answered %d with cookie %q, want %d and none", len(body), response.StatusCode, response.Header.Get("Set-Cookie"), want)
		}
	}
	if seen.reached != 0 {
		t.Errorf("the app was reached %d times by logins, want none: the preview gate answers them", seen.reached)
	}
}

func TestAPreviewGatedResponseIsPrivateWhateverTheAppSaidSoNoProxyInFrontStoresIt(t *testing.T) {
	t.Parallel()
	for name, test := range map[string]struct {
		app  http.Header
		want string
	}{
		"none":         {http.Header{}, "private"},
		"public":       {http.Header{"Cache-Control": {"public, max-age=60, s-maxage=3600"}}, "private, max-age=60"},
		"already":      {http.Header{"Cache-Control": {"private, max-age=5"}}, "private, max-age=5"},
		"no-store":     {http.Header{"Cache-Control": {"no-store"}}, "no-store"},
		"several":      {http.Header{"Cache-Control": {"max-age=60", "public"}}, "max-age=60, private"},
		"cdn override": {http.Header{"Cache-Control": {"public"}, "Cdn-Cache-Control": {"max-age=60"}, "Cloudflare-Cdn-Cache-Control": {"max-age=60"}, "Surrogate-Control": {"max-age=60"}}, "private"},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			at, _ := previewGatedBoard(t, test.app)

			said := ask(t, boardClient, at, previewHost, "/", previewgate.HeaderBypass, previewSalt)

			if got := strings.Join(said.header.Values("Cache-Control"), ", "); got != test.want {
				t.Errorf("Cache-Control = %q, want %q", got, test.want)
			}
			for _, override := range []string{"Cdn-Cache-Control", "Cloudflare-Cdn-Cache-Control", "Surrogate-Control"} {
				if got := said.header.Get(override); got != "" {
					t.Errorf("%s = %q, want it dropped: it overrides Cache-Control at a proxy in front", override, got)
				}
			}
		})
	}
}

func TestAPreviewHostWhoseAppIsDownAnswersABadGatewayThatIsPrivateAndNoindex(t *testing.T) {
	t.Parallel()
	gone, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unreachable := gone.Addr().String()
	_ = gone.Close()
	_, at := served(t, previewGatedRouting(t, shopPreviewGate, unreachable, backend(t, "plain")))

	said := ask(t, boardClient, at, previewHost, "/", previewgate.HeaderBypass, previewSalt)

	if said.status != http.StatusBadGateway {
		t.Fatalf("answered %d, want 502", said.status)
	}
	if got := said.header.Get("Cache-Control"); got != "private" {
		t.Errorf("the 502 carries Cache-Control %q, want private so a proxy in front never stores it", got)
	}
	if got := said.header.Get("X-Robots-Tag"); got != "noindex" {
		t.Errorf("the 502 carries X-Robots-Tag %q, want noindex on every preview response", got)
	}
}

func TestAHostWhoseOwnerHoldsNoPreviewGateIsServedAsBefore(t *testing.T) {
	t.Parallel()
	at, _ := previewGatedBoard(t, nil)

	said := ask(t, boardClient, at, openHost, "/")

	if said.status != http.StatusOK || said.body != "plain" || said.header.Get("X-Robots-Tag") != "" {
		t.Errorf("a host with no preview gate answered %d %q with X-Robots-Tag %q, want the upstream's page untouched", said.status, said.body, said.header.Get("X-Robots-Tag"))
	}
}

func TestAnUnclaimedPreviewHostStaysANotFoundAndLeaksNoLoginForm(t *testing.T) {
	t.Parallel()
	at, _ := previewGatedBoard(t, nil)

	if said := ask(t, boardClient, at, "nobody.preview.example.com", "/"); said.status != http.StatusNotFound {
		t.Errorf("an unclaimed host answered %d, want 404", said.status)
	}
}

func TestAPreviewGateWithNoKeyIsNotLoadedBecauseItsSignaturesWouldBeForgeable(t *testing.T) {
	t.Parallel()

	if _, err := switchboard.Read(previewGatedRouting(t, previewgate.Gate{PasswordMAC: "x"}, "a:1", "b:1")); err == nil {
		t.Fatal("Read() accepted a preview gate with no key, want it refused")
	}
}

func TestAPreviewGateLandsBeforeTheFirstRouteOfItsOwner(t *testing.T) {
	t.Parallel()

	document := `{"grace":"30s","previewGates":[{"owner":"ocel--ghost--preview","previewGate":{"key":"k","passwordMac":"","bypassSecrets":[],"allowOptions":[]}}]}`
	if _, err := switchboard.Read([]byte(document)); err != nil {
		t.Errorf("Read() = %v, want a preview gate for an owner with no routes yet accepted", err)
	}
}

func TestAnOptionsRequestToAnAllowedPrefixPassesThePreviewGateWithoutCredentials(t *testing.T) {
	t.Parallel()
	previewGate := shopPreviewGate
	previewGate.AllowOptions = []string{"/api"}
	app, seen := appBehindPreviewGate(t, nil)
	_, at := served(t, previewGatedRouting(t, previewGate, app, backend(t, "plain")))

	request, err := http.NewRequest(http.MethodOptions, "http://"+at+"/api/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = previewHost
	response, err := boardClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	if response.StatusCode != http.StatusOK || seen.reached != 1 {
		t.Errorf("OPTIONS /api/x answered %d with the app reached %d times, want 200 and once", response.StatusCode, seen.reached)
	}
}
