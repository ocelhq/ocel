package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/envgate"
	"github.com/ocelhq/ocel/cli/internal/manifestbuilder"
	"github.com/ocelhq/ocel/cli/internal/projectconfig"
	"github.com/ocelhq/ocel/cli/internal/providerrunner"
	"github.com/ocelhq/ocel/cli/internal/varsui"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func terminalStdin(d *deps) {
	d.stdinIsTerminal = func(io.Reader) bool { return true }
}

func recordBrowser(d *deps, opened *[]string, mu *sync.Mutex) {
	d.openBrowser = func(url string) error {
		mu.Lock()
		defer mu.Unlock()
		*opened = append(*opened, url)
		return nil
	}
}

type varsUISessions struct {
	mu  sync.Mutex
	all []*varsui.Session
}

func captureVarsUI(d *deps) *varsUISessions {
	sessions := &varsUISessions{}
	prev := d.serveVarsUI
	d.serveVarsUI = func(ctx context.Context, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor, runner *providerrunner.Runner, preview bool, gate *envgate.Gate) (*varsui.Session, error) {
		session, err := prev(ctx, cfg, provider, runner, preview, gate)
		if err == nil {
			sessions.mu.Lock()
			sessions.all = append(sessions.all, session)
			sessions.mu.Unlock()
		}
		return session, err
	}
	return sessions
}

func (s *varsUISessions) abandon(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		count := len(s.all)
		var session *varsui.Session
		if count >= n {
			session = s.all[n-1]
		}
		s.mu.Unlock()
		if session != nil {
			_ = session.Close()
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("session %d never opened", n)
}

var varsUIURL = regexp.MustCompile(`http://127\.0\.0\.1:\d+/#t=[A-Za-z0-9_-]+`)

func awaitVarsUI(t *testing.T, out *syncBuffer, n int) (address, token string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if urls := varsUIURL.FindAllString(out.String(), -1); len(urls) >= n {
			address, token, _ = strings.Cut(urls[n-1], "/#t=")
			return address, token
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("no variables UI URL %d appeared; stdout = %s", n, out.String())
	return "", ""
}

func setCell(t *testing.T, address, token, key, value string) {
	t.Helper()
	body := strings.NewReader(`{"key":"` + key + `","folder":"","value":"` + value + `"}`)
	req, err := http.NewRequest(http.MethodPut, address+"/api/value", body)
	if err != nil {
		t.Fatalf("build the write request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("write %s through the UI: %v", key, err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		detail, _ := io.ReadAll(res.Body)
		t.Fatalf("write %s = %d, want 200: %s", key, res.StatusCode, detail)
	}
}

func markDone(t *testing.T, address, token string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, address+"/api/done", nil)
	if err != nil {
		t.Fatalf("build the done request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("mark the matrix done: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusOK {
		t.Fatalf("done = %d, want 200", res.StatusCode)
	}
}

func problemsFile(t *testing.T, problems string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "problems.json")
	writeFile(t, path, problems)
	t.Setenv("OCEL_TEST_ENV_PROBLEMS_FILE", path)
	return path
}

func varsUISubstrate(t *testing.T, address, token string) string {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, address+"/api/state", nil)
	if err != nil {
		t.Fatalf("build the state request: %v", err)
	}
	req.Header.Set("Authorization", "Bearer "+token)
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("read the page's state: %v", err)
	}
	defer res.Body.Close()
	var state struct {
		Substrate string `json:"substrate"`
	}
	if err := json.NewDecoder(res.Body).Decode(&state); err != nil {
		t.Fatalf("decode the page's state: %v", err)
	}
	return state.Substrate
}

const missingStripeKey = `[{"key":"STRIPE_API_KEY","folder":"","kind":"KIND_MISSING"}]`

func TestGateRecoveryOnDeploy(t *testing.T) {
	t.Run("a gate refusal in a terminal opens the UI and resumes into the build", func(t *testing.T) {
		root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		problems := problemsFile(t, missingStripeKey)
		d := defaultDeps()
		terminalStdin(&d)
		var mu sync.Mutex
		var opened []string
		recordBrowser(&d, &opened, &mu)

		built := false
		stubAppBuildRecorder(&d, &built)

		var out syncBuffer
		var stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- runDeploy(context.Background(), d, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
		}()

		address, token := awaitVarsUI(t, &out, 1)
		setCell(t, address, token, "STRIPE_API_KEY", "sk_live_filled_in")
		writeFile(t, problems, "[]")
		markDone(t, address, token)

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("runDeploy err = %v, want the deploy to resume into the build; stdout=%s stderr=%s", err, out.String(), stderr.String())
			}
		case <-time.After(60 * time.Second):
			t.Fatal("runDeploy never returned after the matrix was marked done")
		}

		if !strings.Contains(out.String(), "Deployed") {
			t.Errorf("stdout = %q, want the resumed deploy to have completed", out.String())
		}
		if !built {
			t.Error("the app was never built, so the deploy did not resume into the build")
		}
		if !strings.Contains(out.String(), "STRIPE_API_KEY") {
			t.Errorf("stdout = %q, want the waiting state to name the cell that stopped the deploy", out.String())
		}
		mu.Lock()
		defer mu.Unlock()
		if len(opened) != 1 || opened[0] != address+"/#t="+token {
			t.Errorf("opened = %v, want the session's own URL opened exactly once", opened)
		}
	})

	t.Run("the resumed pass declares each variable once", func(t *testing.T) {
		root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
		problems := problemsFile(t, missingStripeKey)
		d := defaultDeps()
		terminalStdin(&d)
		var mu sync.Mutex
		var opened []string
		recordBrowser(&d, &opened, &mu)
		stubAppFunctions(&d, []manifestbuilder.Function{
			{Name: "api", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
		})

		var out syncBuffer
		var stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- runDeploy(context.Background(), d, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
		}()

		address, token := awaitVarsUI(t, &out, 1)
		setCell(t, address, token, "STRIPE_API_KEY", "pk_filled_in")
		writeFile(t, problems, "[]")
		markDone(t, address, token)

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, out.String(), stderr.String())
			}
		case <-time.After(60 * time.Second):
			t.Fatal("runDeploy never returned after the matrix was marked done")
		}

		got := out.String()
		if !strings.Contains(got, "vars=STRIPE_API_KEY\n") {
			t.Errorf("stdout = %q, want the resumed manifest to carry STRIPE_API_KEY exactly once", got)
		}
		if strings.Contains(got, "pk_filled_in") {
			t.Errorf("stdout = %q, want no variable value ever printed", got)
		}
	})

	t.Run("the waiting state says how to abort", func(t *testing.T) {
		root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		problemsFile(t, missingStripeKey)
		d := defaultDeps()
		terminalStdin(&d)
		var mu sync.Mutex
		var opened []string
		recordBrowser(&d, &opened, &mu)
		built := false
		stubAppBuildRecorder(&d, &built)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var out syncBuffer
		var stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- runDeploy(ctx, d, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
		}()

		awaitVarsUI(t, &out, 1)
		waiting := out.String()
		for _, want := range []string{"Waiting", "Ctrl-C"} {
			if !strings.Contains(waiting, want) {
				t.Errorf("stdout = %q, want the waiting state to contain %q", waiting, want)
			}
		}
		if strings.Contains(waiting, "run this command again") {
			t.Errorf("stdout = %q, want a waiting command not to tell the developer to re-run it", waiting)
		}
		cancel()
		<-done
	})

	t.Run("interrupting while waiting aborts with nothing built", func(t *testing.T) {
		root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		problemsFile(t, missingStripeKey)
		d := defaultDeps()
		terminalStdin(&d)
		var mu sync.Mutex
		var opened []string
		recordBrowser(&d, &opened, &mu)
		built := false
		stubAppBuildRecorder(&d, &built)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var out syncBuffer
		var stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- runDeploy(ctx, d, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
		}()

		awaitVarsUI(t, &out, 1)
		cancel()

		select {
		case err := <-done:
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Code == 0 {
				t.Fatalf("runDeploy err = %v, want a non-zero exit; stdout=%s", err, out.String())
			}
		case <-time.After(60 * time.Second):
			t.Fatal("runDeploy never returned after the context was cancelled")
		}

		if built {
			t.Error("the app was built, want an interrupted wait to build nothing")
		}
		if strings.Contains(out.String(), "DEPLOY ") {
			t.Errorf("stdout = %q, want no Deploy to have been driven", out.String())
		}
		if strings.Contains(out.String(), "Resources may be partially created") {
			t.Errorf("stdout = %q, want an interrupted wait to say nothing was provisioned", out.String())
		}
	})

	t.Run("closing the UI still names the keys that are owed", func(t *testing.T) {
		root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		problemsFile(t, missingStripeKey)
		d := defaultDeps()
		terminalStdin(&d)
		var mu sync.Mutex
		var opened []string
		recordBrowser(&d, &opened, &mu)
		sessions := captureVarsUI(&d)
		built := false
		stubAppBuildRecorder(&d, &built)

		var out syncBuffer
		var stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- runDeploy(context.Background(), d, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
		}()

		awaitVarsUI(t, &out, 1)
		before := out.String()
		sessions.abandon(t, 1)

		select {
		case err := <-done:
			var exit *ExitError
			if !errors.As(err, &exit) || exit.Code == 0 {
				t.Fatalf("runDeploy err = %v, want a non-zero exit", err)
			}
		case <-time.After(60 * time.Second):
			t.Fatal("runDeploy never returned after the UI was abandoned")
		}

		tail := strings.TrimPrefix(out.String(), before) + stderr.String()
		for _, want := range []string{"STRIPE_API_KEY", "ocel env set STRIPE_API_KEY <VALUE>", "closed before the matrix was complete"} {
			if !strings.Contains(tail, want) {
				t.Errorf("output after the UI closed = %q, want it to contain %q", tail, want)
			}
		}
		if built {
			t.Error("the app was built, want an abandoned wait to build nothing")
		}
	})

	t.Run("a replacement that still fails the schema does not slip through", func(t *testing.T) {
		root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		envSet(t, root, "STRIPE_API_KEY", "nope", envOptions{})
		problemsFile(t, `[{"key":"STRIPE_API_KEY","folder":"","kind":"KIND_INVALID","detail":"must start with sk_"}]`)
		d := defaultDeps()
		terminalStdin(&d)
		var mu sync.Mutex
		var opened []string
		recordBrowser(&d, &opened, &mu)
		sessions := captureVarsUI(&d)
		built := false
		stubAppBuildRecorder(&d, &built)

		var out syncBuffer
		var stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- runDeploy(context.Background(), d, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
		}()

		address, token := awaitVarsUI(t, &out, 1)
		setCell(t, address, token, "STRIPE_API_KEY", "also_nope")
		markDone(t, address, token)

		awaitVarsUI(t, &out, 2)
		sessions.abandon(t, 2)

		select {
		case err := <-done:
			if err == nil {
				t.Fatalf("runDeploy err = nil, want the second invalid value refused; stdout=%s", out.String())
			}
		case <-time.After(60 * time.Second):
			t.Fatal("runDeploy never returned")
		}
		if built {
			t.Error("the app was built with a value the schema rejects")
		}
		if strings.Contains(out.String(), "Deployed") {
			t.Errorf("stdout = %q, want no deploy to have completed", out.String())
		}
		if strings.Contains(out.String(), "also_nope") || strings.Contains(out.String(), "nope") {
			t.Errorf("stdout = %q, want no variable value ever printed", out.String())
		}
	})

	t.Run("a reopened UI shows what is still owed", func(t *testing.T) {
		root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true},{"key":"DATABASE_URL","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		problems := problemsFile(t, `[{"key":"STRIPE_API_KEY","folder":"","kind":"KIND_MISSING"},{"key":"DATABASE_URL","folder":"","kind":"KIND_MISSING"}]`)
		d := defaultDeps()
		terminalStdin(&d)
		var mu sync.Mutex
		var opened []string
		recordBrowser(&d, &opened, &mu)
		sessions := captureVarsUI(&d)
		built := false
		stubAppBuildRecorder(&d, &built)

		var out syncBuffer
		var stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- runDeploy(context.Background(), d, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
		}()

		address, token := awaitVarsUI(t, &out, 1)
		if first := out.String(); !strings.Contains(first, "STRIPE_API_KEY") || !strings.Contains(first, "DATABASE_URL") {
			t.Fatalf("stdout = %q, want the first refusal to name both cells", first)
		}
		setCell(t, address, token, "STRIPE_API_KEY", "sk_live_filled_in")
		writeFile(t, problems, `[{"key":"DATABASE_URL","folder":"","kind":"KIND_MISSING"}]`)
		before := out.String()
		markDone(t, address, token)

		awaitVarsUI(t, &out, 2)
		reopened := strings.TrimPrefix(out.String(), before)
		if !strings.Contains(reopened, "DATABASE_URL") {
			t.Errorf("reopened block = %q, want it to name the cell that is still owed", reopened)
		}
		if strings.Contains(reopened, "STRIPE_API_KEY") {
			t.Errorf("reopened block = %q, want a cell the developer already filled not shown as owed", reopened)
		}

		sessions.abandon(t, 2)
		select {
		case err := <-done:
			if err == nil {
				t.Fatal("runDeploy err = nil, want the still-incomplete matrix refused")
			}
		case <-time.After(60 * time.Second):
			t.Fatal("runDeploy never returned")
		}
		if built {
			t.Error("the app was built with a required variable still missing")
		}
	})

	t.Run("a non-interactive run never waits", func(t *testing.T) {
		root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		t.Setenv("OCEL_TEST_ENV_PROBLEMS", missingStripeKey)
		d := defaultDeps()
		var mu sync.Mutex
		var opened []string
		recordBrowser(&d, &opened, &mu)
		built := false
		stubAppBuildRecorder(&d, &built)

		ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
		defer cancel()
		var stdout, stderr bytes.Buffer
		err := runDeploy(ctx, d, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
		if err == nil {
			t.Fatal("runDeploy err = nil, want a non-interactive run to hard-fail")
		}
		if varsUIURL.MatchString(stdout.String()) {
			t.Errorf("stdout = %q, want no variables UI opened without a terminal", stdout.String())
		}
		mu.Lock()
		defer mu.Unlock()
		if len(opened) != 0 {
			t.Errorf("opened = %v, want no browser launched without a terminal", opened)
		}
	})

	t.Run("the opt-outs keep a terminal from waiting", func(t *testing.T) {
		for _, tc := range []struct {
			name string
			opts deployOptions
			env  string
		}{
			{name: "--no-ui", opts: deployOptions{yes: true, noUI: true}},
			{name: noBrowserEnvVar, opts: deployOptions{yes: true}, env: "1"},
			{name: noBrowserEnvVar + "=anything", opts: deployOptions{yes: true}, env: "true"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
				t.Setenv("OCEL_TEST_ENV_PROBLEMS", missingStripeKey)
				t.Setenv(noBrowserEnvVar, tc.env)
				d := defaultDeps()
				terminalStdin(&d)
				var mu sync.Mutex
				var opened []string
				recordBrowser(&d, &opened, &mu)
				built := false
				stubAppBuildRecorder(&d, &built)

				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				var stdout, stderr bytes.Buffer
				err := runDeploy(ctx, d, root, tc.opts, &stdout, &stderr, strings.NewReader(""))
				if err == nil {
					t.Fatal("runDeploy err = nil, want the opt-out to keep the hard refusal")
				}
				if varsUIURL.MatchString(stdout.String()) {
					t.Errorf("stdout = %q, want no variables UI opened", stdout.String())
				}
				if built {
					t.Error("the app was built, want the gate to refuse before any build runs")
				}
			})
		}
	})
}

func TestGateRecoveryOnPreviewUp(t *testing.T) {
	t.Run("a gate refusal in a terminal opens the UI and resumes", func(t *testing.T) {
		root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		t.Setenv(fakeInfraClassEnvVar, "preview")
		problems := problemsFile(t, missingStripeKey)
		d := defaultDeps()
		terminalStdin(&d)
		var mu sync.Mutex
		var opened []string
		recordBrowser(&d, &opened, &mu)
		built := false
		stubAppBuildRecorder(&d, &built)

		var out syncBuffer
		var stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			done <- runPreviewUp(context.Background(), d, root, previewUpOptions{name: "staging"}, &out, &stderr, strings.NewReader(""))
		}()

		address, token := awaitVarsUI(t, &out, 1)
		if got := varsUISubstrate(t, address, token); got != "preview" {
			t.Errorf("substrate = %q, want the preview's own", got)
		}
		setCell(t, address, token, "STRIPE_API_KEY", "sk_live_filled_in")
		writeFile(t, problems, "[]")
		markDone(t, address, token)

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("runPreviewUp err = %v, want the preview to resume; stdout=%s stderr=%s", err, out.String(), stderr.String())
			}
		case <-time.After(60 * time.Second):
			t.Fatal("runPreviewUp never returned after the matrix was marked done")
		}
		if !built {
			t.Error("the app was never built, so the preview did not resume into the build")
		}
	})

	t.Run("the opt-outs and a non-terminal keep the hard refusal", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			terminal bool
			opts     previewUpOptions
		}{
			{name: "no terminal", opts: previewUpOptions{name: "staging"}},
			{name: "--no-ui", terminal: true, opts: previewUpOptions{name: "staging", noUI: true}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
				t.Setenv(fakeInfraClassEnvVar, "preview")
				t.Setenv("OCEL_TEST_ENV_PROBLEMS", missingStripeKey)
				d := defaultDeps()
				if tc.terminal {
					terminalStdin(&d)
				}
				var mu sync.Mutex
				var opened []string
				recordBrowser(&d, &opened, &mu)
				built := false
				stubAppBuildRecorder(&d, &built)

				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				var stdout, stderr bytes.Buffer
				err := runPreviewUp(ctx, d, root, tc.opts, &stdout, &stderr, strings.NewReader(""))
				if err == nil {
					t.Fatal("runPreviewUp err = nil, want the hard refusal kept")
				}
				if varsUIURL.MatchString(stdout.String()) {
					t.Errorf("stdout = %q, want no variables UI opened", stdout.String())
				}
				if built {
					t.Error("the app was built, want the gate to refuse before any build runs")
				}
				mu.Lock()
				defer mu.Unlock()
				if len(opened) != 0 {
					t.Errorf("opened = %v, want no browser launched", opened)
				}
			})
		}
	})
}

func TestAbandonedRefusal(t *testing.T) {
	t.Parallel()

	t.Run("matches both the refusal and the abandonment", func(t *testing.T) {
		t.Parallel()

		refusal := &envgate.Refusal{Problems: []*resourcesv1.VariableProblem{
			{Key: "STRIPE_API_KEY", Kind: resourcesv1.VariableProblem_KIND_MISSING},
		}}
		var err error = &abandonedRefusal{refusal: refusal}

		if !errors.Is(err, varsui.ErrAbandoned) {
			t.Error("errors.Is(err, ErrAbandoned) = false, want an abandonment the caller can match")
		}
		var got *envgate.Refusal
		if !errors.As(err, &got) || got != refusal {
			t.Error("errors.As(err, *envgate.Refusal) did not recover the original refusal")
		}
		if !strings.Contains(err.Error(), "STRIPE_API_KEY") {
			t.Errorf("err = %q, want the keys that are owed named", err)
		}
	})
}
