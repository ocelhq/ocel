package switchboard_test

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"maps"
	"net"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/edge/edgeconformance"
	"github.com/ocelhq/ocel/pkg/edge/gate"
	"github.com/ocelhq/ocel/pkg/router"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
	"github.com/ocelhq/ocel/platform/vps/box/switchboard"
)

const (
	previewHost     = "abc12.preview.example.com"
	productionHost  = "shop.example.com"
	openHost        = "plain.example.com"
	shopPreview     = "ocel--shop--preview"
	shopProduction  = "ocel--shop--production"
	blogProduction  = "ocel--blog--production"
	previewPassword = "hunter2"
	previewBypass   = "bypass-secret-1"
	productionPass  = "correct horse"
	productionSkip  = "bypass-secret-2"
)

func gateOf(key, password, bypass string) gate.Gate {
	return gate.Gate{Key: key, PasswordHash: gate.HashPassword(password), BypassSecrets: []string{bypass}}
}

var (
	previewGate    = gateOf("preview-key", previewPassword, previewBypass)
	productionGate = gateOf("production-key", productionPass, productionSkip)
)

type gatedTier struct {
	host     string
	password string
	bypass   string
}

var gatedTiers = map[string]gatedTier{
	"preview":    {host: previewHost, password: previewPassword, bypass: previewBypass},
	"production": {host: productionHost, password: productionPass, bypass: productionSkip},
}

type upstreams struct{ preview, production, open string }

func gatedRouting(t *testing.T, gates map[string]gate.Gate, to upstreams) []byte {
	t.Helper()
	type heldGate struct {
		Owner string    `json:"owner"`
		Gate  gate.Gate `json:"gate"`
	}
	held := []heldGate{}
	for _, owner := range slices.Sorted(maps.Keys(gates)) {
		held = append(held, heldGate{Owner: owner, Gate: gates[owner]})
	}
	document := map[string]any{
		"grace": "30s",
		"claims": []map[string]string{
			{"owner": shopPreview, "hostname": previewHost, "pointer": "pr-1", "app": "web"},
			{"owner": shopProduction, "hostname": productionHost, "pointer": "@production", "app": "web"},
			{"owner": blogProduction, "hostname": openHost, "pointer": "@production", "app": "web"},
		},
		"routes": []map[string]string{
			{"owner": shopPreview, "pointer": "pr-1", "app": "web", "upstream": to.preview},
			{"owner": shopProduction, "pointer": "@production", "app": "web", "upstream": to.production},
			{"owner": blogProduction, "pointer": "@production", "app": "web", "upstream": to.open},
		},
		"gates": held,
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

func appBehindGate(t *testing.T, header http.Header) (string, *seenByApp) {
	t.Helper()
	seen := &seenByApp{}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		seen.reached++
		seen.authorization = r.Header.Get("Authorization")
		seen.cookie = r.Header.Get("Cookie")
		seen.bypass = r.Header.Get(gate.HeaderBypass)
		for name, values := range header {
			w.Header()[name] = values
		}
		_, _ = io.WriteString(w, "the page")
	}))
	t.Cleanup(server.Close)
	return strings.TrimPrefix(server.URL, "http://"), seen
}

var bothGated = map[string]gate.Gate{shopPreview: previewGate, shopProduction: productionGate}

func gatedBoard(t *testing.T, appHeader http.Header) (string, *seenByApp) {
	t.Helper()
	app, seen := appBehindGate(t, appHeader)
	_, at := served(t, gatedRouting(t, bothGated, upstreams{preview: app, production: app, open: backend(t, "plain")}))
	return at, seen
}

func basic(password string) string {
	return "Basic " + base64.StdEncoding.EncodeToString([]byte("anyone:"+password))
}

func postLogin(t *testing.T, at, host, authorization string) *http.Response {
	t.Helper()
	request, err := http.NewRequest(http.MethodPost, "http://"+at+gate.LoginPath, nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = host
	if authorization != "" {
		request.Header.Set("Authorization", authorization)
	}
	response, err := (&http.Client{CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}).Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()
	return response
}

func TestAGatedHostRefusesARequestWithoutCredentialsAndNeverReachesTheApp(t *testing.T) {
	t.Parallel()
	for tier, gated := range gatedTiers {
		t.Run(tier, func(t *testing.T) {
			t.Parallel()
			at, seen := gatedBoard(t, nil)

			said := ask(t, boardClient, at, gated.host, "/cart")

			if said.status != http.StatusUnauthorized {
				t.Fatalf("a request without credentials answered %d, want 401", said.status)
			}
			if got := said.header.Get("Cache-Control"); got != "private, no-store" {
				t.Errorf("the refusal carries Cache-Control %q, want %q so a proxy in front never stores it", got, "private, no-store")
			}
			if got := said.header.Get("X-Robots-Tag"); got != "noindex" {
				t.Errorf("the refusal carries X-Robots-Tag %q, want noindex", got)
			}
			if !strings.Contains(said.body, `fetch("`+gate.LoginPath+`"`) || strings.Contains(said.body, "the page") {
				t.Errorf("the refusal body = %q, want the login form and none of the page", said.body)
			}
			if seen.reached != 0 {
				t.Errorf("the app was reached %d times, want none", seen.reached)
			}
		})
	}
}

func TestAGatedHostServesTheAppToEachCredentialAndStripsWhatTheGateConsumed(t *testing.T) {
	t.Parallel()
	for tier, gated := range gatedTiers {
		for name, credential := range map[string][2]string{
			"Basic credentials": {"Authorization", basic(gated.password)},
			"the bypass header": {gate.HeaderBypass, gated.bypass},
		} {
			t.Run(tier+"/"+name, func(t *testing.T) {
				t.Parallel()
				at, seen := gatedBoard(t, nil)

				said := ask(t, boardClient, at, gated.host, "/cart", credential[0], credential[1])

				if said.status != http.StatusOK || said.body != "the page" {
					t.Fatalf("answered %d %q, want the app's page", said.status, said.body)
				}
				if got := said.header.Get("X-Robots-Tag"); got != "noindex" {
					t.Errorf("X-Robots-Tag = %q, want noindex on every gated response", got)
				}
				if seen.authorization != "" || seen.bypass != "" {
					t.Errorf("the app saw Authorization %q and bypass %q, want the credentials the gate consumed stripped", seen.authorization, seen.bypass)
				}
			})
		}
	}
}

func TestAGatedHostRefusesWrongCredentials(t *testing.T) {
	t.Parallel()
	for tier, gated := range gatedTiers {
		t.Run(tier, func(t *testing.T) {
			t.Parallel()
			at, seen := gatedBoard(t, nil)

			for _, wrong := range [][2]string{
				{"Authorization", basic("wrong")},
				{gate.HeaderBypass, "not-the-secret"},
				{"Cookie", gate.CookieName + "=9999999999." + strings.Repeat("a", 64)},
			} {
				if said := ask(t, boardClient, at, gated.host, "/", wrong[0], wrong[1]); said.status != http.StatusUnauthorized {
					t.Errorf("%s answered %d, want 401", wrong[0], said.status)
				}
			}
			if seen.reached != 0 {
				t.Errorf("the app was reached %d times, want none", seen.reached)
			}
		})
	}
}

func TestTheCredentialsOfOneTierNeverOpenTheOther(t *testing.T) {
	t.Parallel()
	for tier, gated := range gatedTiers {
		t.Run(tier, func(t *testing.T) {
			t.Parallel()
			at, seen := gatedBoard(t, nil)
			for other, theirs := range gatedTiers {
				if other == tier {
					continue
				}
				for name, credential := range map[string][2]string{
					"password": {"Authorization", basic(theirs.password)},
					"bypass":   {gate.HeaderBypass, theirs.bypass},
				} {
					if said := ask(t, boardClient, at, gated.host, "/", credential[0], credential[1]); said.status != http.StatusUnauthorized {
						t.Errorf("the %s %s answered %d on the %s host, want 401", other, name, said.status, tier)
					}
				}
				response := postLogin(t, at, theirs.host, basic(theirs.password))
				cookie, _, _ := strings.Cut(response.Header.Get("Set-Cookie"), ";")
				if said := ask(t, boardClient, at, gated.host, "/", "Cookie", cookie); said.status != http.StatusUnauthorized {
					t.Errorf("the %s session cookie answered %d on the %s host, want 401", other, said.status, tier)
				}
			}
			if seen.reached != 0 {
				t.Errorf("the app was reached %d times, want none", seen.reached)
			}
		})
	}
}

func TestAGateHeldByOneTierNeverGatesTheOtherTierOfTheSameProject(t *testing.T) {
	t.Parallel()
	for held, gates := range map[string]map[string]gate.Gate{
		"preview":    {shopPreview: previewGate},
		"production": {shopProduction: productionGate},
	} {
		t.Run(held, func(t *testing.T) {
			t.Parallel()
			app, _ := appBehindGate(t, nil)
			_, at := served(t, gatedRouting(t, gates, upstreams{preview: app, production: app, open: backend(t, "plain")}))

			for tier, gated := range gatedTiers {
				said := ask(t, boardClient, at, gated.host, "/")
				want := http.StatusOK
				if tier == held {
					want = http.StatusUnauthorized
				}
				if said.status != want {
					t.Errorf("with only the %s gate held, the %s host answered %d, want %d", held, tier, said.status, want)
				}
			}
		})
	}
}

func TestAProductionHostMeetsTheGateBeforeAnythingThatCouldAnswerFromAnEarlierResponse(t *testing.T) {
	t.Parallel()
	at, seen := gatedBoard(t, http.Header{"Cache-Control": {"public, max-age=31536000, immutable"}})

	warm := ask(t, boardClient, at, productionHost, "/app.js", gate.HeaderBypass, productionSkip)
	if warm.status != http.StatusOK || warm.body != "the page" {
		t.Fatalf("the authenticated request answered %d %q, want the app's page", warm.status, warm.body)
	}
	if got := warm.header.Get("Cache-Control"); !strings.HasPrefix(got, "private") {
		t.Errorf("the authenticated answer carries Cache-Control %q, want it private so no proxy in front stores it", got)
	}

	for _, path := range []string{"/app.js", "/app.js?v=1", "/", edge.LivenessProbePath} {
		said := ask(t, boardClient, at, productionHost, path)
		if said.status != http.StatusUnauthorized || strings.Contains(said.body, "the page") {
			t.Errorf("an unauthenticated %s after a served one answered %d %q, want 401 and the login", path, said.status, said.body)
		}
		if got := said.header.Get("Cache-Control"); got != "private, no-store" {
			t.Errorf("the refusal of %s carries Cache-Control %q, want %q", path, got, "private, no-store")
		}
	}
	if seen.reached != 1 {
		t.Errorf("the app was reached %d times, want only the authenticated request", seen.reached)
	}
}

func TestTheLivenessProbeOfAGatedProductionHostStillHearsTheSwitchboardWithoutReachingTheApp(t *testing.T) {
	t.Parallel()
	at, seen := gatedBoard(t, nil)

	said := ask(t, boardClient, at, productionHost, edge.LivenessProbePath)

	if said.status != http.StatusUnauthorized {
		t.Fatalf("the probe answered %d, want the gate's 401", said.status)
	}
	if got := said.header.Get(router.HeaderRouter); got != string(boxapi.SwitchboardRouter) {
		t.Errorf("the probe's answer names router %q, want %q: the host check reads it to know this box answers", got, boxapi.SwitchboardRouter)
	}
	if said.header.Get(boxapi.HeardHeader) == "" {
		t.Errorf("the probe's answer carries no %s, want what the switchboard heard", boxapi.HeardHeader)
	}
	if seen.reached != 0 {
		t.Errorf("the app was reached %d times, want none", seen.reached)
	}
}

func TestABearerTokenPassesThroughToTheAppButDoesNotOpenTheGate(t *testing.T) {
	t.Parallel()
	at, seen := gatedBoard(t, nil)

	if said := ask(t, boardClient, at, productionHost, "/api", "Authorization", "Bearer token"); said.status != http.StatusUnauthorized {
		t.Fatalf("a bearer token alone answered %d, want 401", said.status)
	}
	said := ask(t, boardClient, at, productionHost, "/api", "Authorization", "Bearer token", gate.HeaderBypass, productionSkip)
	if said.status != http.StatusOK || seen.authorization != "Bearer token" {
		t.Errorf("answered %d with the app seeing Authorization %q, want 200 and the bearer token untouched", said.status, seen.authorization)
	}
}

func TestALoginPostAnswersTheCookieThatOpensTheHostAndTheAppNeverSeesIt(t *testing.T) {
	t.Parallel()
	for tier, gated := range gatedTiers {
		t.Run(tier, func(t *testing.T) {
			t.Parallel()
			at, seen := gatedBoard(t, nil)

			response := postLogin(t, at, gated.host, basic(gated.password))

			if response.StatusCode != http.StatusNoContent {
				t.Fatalf("the login answered %d, want 204", response.StatusCode)
			}
			if got := response.Header.Get("Cache-Control"); got != "private, no-store" {
				t.Errorf("the login answer carries Cache-Control %q, want %q", got, "private, no-store")
			}
			cookie, _, _ := strings.Cut(response.Header.Get("Set-Cookie"), ";")
			if !strings.HasPrefix(cookie, gate.CookieName+"=") {
				t.Fatalf("Set-Cookie = %q, want the session cookie", response.Header.Get("Set-Cookie"))
			}
			said := ask(t, boardClient, at, gated.host, "/cart", "Cookie", cookie+"; theme=dark")
			if said.status != http.StatusOK {
				t.Fatalf("the cookie the login set answered %d, want 200", said.status)
			}
			if seen.cookie != "theme=dark" {
				t.Errorf("the app saw Cookie %q, want only the cookies the gate did not consume", seen.cookie)
			}
		})
	}
}

func TestALoginPostWithAWrongOrMissingPasswordOpensNothing(t *testing.T) {
	t.Parallel()
	at, seen := gatedBoard(t, nil)

	for _, gated := range gatedTiers {
		for name, authorization := range map[string]string{"wrong": basic("wrong"), "missing": ""} {
			response := postLogin(t, at, gated.host, authorization)
			if response.StatusCode != http.StatusUnauthorized || response.Header.Get("Set-Cookie") != "" {
				t.Errorf("a %s password on %s answered %d with cookie %q, want 401 and none", name, gated.host, response.StatusCode, response.Header.Get("Set-Cookie"))
			}
		}
	}
	if seen.reached != 0 {
		t.Errorf("the app was reached %d times by logins, want none: the gate answers them", seen.reached)
	}
}

func TestAGatedResponseIsPrivateWhateverTheAppSaidSoNoProxyInFrontStoresIt(t *testing.T) {
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
		for tier, gated := range gatedTiers {
			t.Run(tier+"/"+name, func(t *testing.T) {
				t.Parallel()
				at, _ := gatedBoard(t, test.app)

				said := ask(t, boardClient, at, gated.host, "/", gate.HeaderBypass, gated.bypass)

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
}

func TestAGatedHostWhoseAppIsDownAnswersABadGatewayThatIsPrivateAndNoindex(t *testing.T) {
	t.Parallel()
	gone, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	unreachable := gone.Addr().String()
	_ = gone.Close()
	_, at := served(t, gatedRouting(t, bothGated, upstreams{preview: unreachable, production: unreachable, open: backend(t, "plain")}))

	for tier, gated := range gatedTiers {
		said := ask(t, boardClient, at, gated.host, "/", gate.HeaderBypass, gated.bypass)

		if said.status != http.StatusBadGateway {
			t.Fatalf("the %s host answered %d, want 502", tier, said.status)
		}
		if got := said.header.Get("Cache-Control"); got != "private" {
			t.Errorf("the %s host's 502 carries Cache-Control %q, want private so a proxy in front never stores it", tier, got)
		}
		if got := said.header.Get("X-Robots-Tag"); got != "noindex" {
			t.Errorf("the %s host's 502 carries X-Robots-Tag %q, want noindex on every gated response", tier, got)
		}
	}
}

func TestAHostWhoseOwnerHoldsNoGateIsServedAsBefore(t *testing.T) {
	t.Parallel()
	at, _ := gatedBoard(t, nil)

	said := ask(t, boardClient, at, openHost, "/")

	if said.status != http.StatusOK || said.body != "plain" || said.header.Get("X-Robots-Tag") != "" {
		t.Errorf("a host with no gate answered %d %q with X-Robots-Tag %q, want the upstream's page untouched", said.status, said.body, said.header.Get("X-Robots-Tag"))
	}
}

func TestAnUnclaimedPreviewHostStaysANotFoundAndLeaksNoLoginForm(t *testing.T) {
	t.Parallel()
	at, _ := gatedBoard(t, nil)

	if said := ask(t, boardClient, at, "nobody.preview.example.com", "/"); said.status != http.StatusNotFound {
		t.Errorf("an unclaimed host answered %d, want 404", said.status)
	}
}

func TestAGateWithNoKeyIsNotLoadedBecauseItsSignaturesWouldBeForgeable(t *testing.T) {
	t.Parallel()

	for _, owner := range []string{shopPreview, shopProduction} {
		if _, err := boxapi.ReadTable(gatedRouting(t, map[string]gate.Gate{owner: {PasswordHash: "x"}}, upstreams{"a:1", "b:1", "c:1"})); err == nil {
			t.Errorf("Read() accepted a gate with no key for %s, want it refused", owner)
		}
	}
}

func TestAGateLandsBeforeTheFirstRouteOfItsOwner(t *testing.T) {
	t.Parallel()

	for _, owner := range []string{"ocel--ghost--preview", "ocel--ghost--production"} {
		document := `{"grace":"30s","gates":[{"owner":"` + owner + `","gate":{"key":"k","passwordHash":"","bypassSecrets":[],"allowOptions":[]}}]}`
		if _, err := boxapi.ReadTable([]byte(document)); err != nil {
			t.Errorf("Read() = %v, want a gate for %s, which has no routes yet, accepted", err, owner)
		}
	}
}

func TestAnOptionsRequestToAnAllowedPrefixPassesTheGateWithoutCredentials(t *testing.T) {
	t.Parallel()
	allowing := productionGate
	allowing.AllowOptions = []string{"/api"}
	app, seen := appBehindGate(t, nil)
	_, at := served(t, gatedRouting(t, map[string]gate.Gate{shopProduction: allowing}, upstreams{preview: app, production: app, open: backend(t, "plain")}))

	request, err := http.NewRequest(http.MethodOptions, "http://"+at+"/api/x", nil)
	if err != nil {
		t.Fatal(err)
	}
	request.Host = productionHost
	response, err := boardClient.Do(request)
	if err != nil {
		t.Fatal(err)
	}
	_ = response.Body.Close()

	if response.StatusCode != http.StatusOK || seen.reached != 1 {
		t.Errorf("OPTIONS /api/x answered %d with the app reached %d times, want 200 and once", response.StatusCode, seen.reached)
	}
}

func TestTheSwitchboardsGateAnswersTheGatesConformanceVectorsOverHTTP(t *testing.T) {
	edgeconformance.RunGateWith(t, switchboard.CheckGateOverHTTP)
}
