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

// The recovery path is the one place the CLI blocks on a browser, so its tests
// have to drive both ends at once: runDeploy on one goroutine, and the page's
// own API on the other. Everything below is that harness.

// terminalStdin makes the run interactive. Every test drives the CLI with an
// in-memory reader, so the seam is the only way to reach the waiting path.
func terminalStdin(t *testing.T) {
	t.Helper()
	prev := stdinIsTerminal
	stdinIsTerminal = func(io.Reader) bool { return true }
	t.Cleanup(func() { stdinIsTerminal = prev })
}

// recordBrowser stops the test opening the developer's real browser and
// records what it was asked to open.
func recordBrowser(t *testing.T, opened *[]string, mu *sync.Mutex) {
	t.Helper()
	prev := openBrowser
	openBrowser = func(url string) error {
		mu.Lock()
		defer mu.Unlock()
		*opened = append(*opened, url)
		return nil
	}
	t.Cleanup(func() { openBrowser = prev })
}

// varsUISessions holds every UI session a run opened. Ending one without a
// completion is what an abandonment is, and nothing inside the process does it:
// the page sends no signal when the browser closes, so holding the session is
// the only way a test reaches that outcome.
type varsUISessions struct {
	mu  sync.Mutex
	all []*varsui.Session
}

func captureVarsUI(t *testing.T) *varsUISessions {
	t.Helper()
	sessions := &varsUISessions{}
	prev := serveVarsUI
	serveVarsUI = func(ctx context.Context, cfg *projectconfig.Config, provider *projectconfig.ProviderDescriptor, runner *providerrunner.Runner, preview bool, gate *envgate.Gate) (*varsui.Session, error) {
		session, err := prev(ctx, cfg, provider, runner, preview, gate)
		if err == nil {
			sessions.mu.Lock()
			sessions.all = append(sessions.all, session)
			sessions.mu.Unlock()
		}
		return session, err
	}
	t.Cleanup(func() { serveVarsUI = prev })
	return sessions
}

// abandon closes the nth session the way a closed browser leaves one: no
// completion, no interrupt.
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

// varsUIURL is the URL the waiting state prints, with the session token in the
// fragment exactly as the page reads it.
var varsUIURL = regexp.MustCompile(`http://127\.0\.0\.1:\d+/#t=[A-Za-z0-9_-]+`)

// awaitVarsUI blocks until the run has printed the nth variables-UI URL, and
// splits it into the address and token an API call needs.
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

// setCell writes one value through the page's own API, which is how a
// developer's write reaches the gate the deploy is holding.
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

// markDone is the page's "I'm finished" button.
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

// problemsFile points the fixture's declaring process at a file it re-reads on
// every discovery pass, so a test can change what the second pass reports —
// which is the only way to tell a re-validated resume from a re-checked one.
func problemsFile(t *testing.T, problems string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "problems.json")
	writeFile(t, path, problems)
	t.Setenv("OCEL_TEST_ENV_PROBLEMS_FILE", path)
	return path
}

const missingStripeKey = `[{"key":"STRIPE_API_KEY","folder":"","kind":"KIND_MISSING"}]`

// TestRunDeploy_AGateRefusalInATerminalOpensTheUIAndResumesIntoTheBuild is the
// whole point of the recovery: the developer never re-runs the command, and
// never repeats the confirmation they already gave.
func TestRunDeploy_AGateRefusalInATerminalOpensTheUIAndResumesIntoTheBuild(t *testing.T) {
	root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
	problems := problemsFile(t, missingStripeKey)
	terminalStdin(t)
	var mu sync.Mutex
	var opened []string
	recordBrowser(t, &opened, &mu)

	built := false
	stubAppBuildRecorder(t, &built)

	var out syncBuffer
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runDeploy(context.Background(), root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
	}()

	address, token := awaitVarsUI(t, &out, 1)
	setCell(t, address, token, "STRIPE_API_KEY", "sk_live_filled_in")
	// The second discovery pass has nothing left to report.
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
}

// TestRunDeploy_TheResumedPassDeclaresEachVariableOnce: the retry runs
// discovery a second time, and a gate accumulates declarations — so the second
// pass has to get its own. Reusing the first one would deploy every app with
// each of its variables twice.
func TestRunDeploy_TheResumedPassDeclaresEachVariableOnce(t *testing.T) {
	root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
	problems := problemsFile(t, missingStripeKey)
	terminalStdin(t)
	var mu sync.Mutex
	var opened []string
	recordBrowser(t, &opened, &mu)
	stubAppFunctions(t, []manifestbuilder.Function{
		{Name: "api", Runtime: "nodejs24.x", Handler: "src/server.js", ArtifactPath: "output/api", Framework: "express", App: "api"},
	})

	var out syncBuffer
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runDeploy(context.Background(), root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
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
}

// TestRunDeploy_TheWaitingStateSaysHowToAbort pins the half of the waiting
// state that is not the URL: a blocked command has to say it is blocked, and
// what to press.
func TestRunDeploy_TheWaitingStateSaysHowToAbort(t *testing.T) {
	root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
	problemsFile(t, missingStripeKey)
	terminalStdin(t)
	var mu sync.Mutex
	var opened []string
	recordBrowser(t, &opened, &mu)
	built := false
	stubAppBuildRecorder(t, &built)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out syncBuffer
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runDeploy(ctx, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
	}()

	awaitVarsUI(t, &out, 1)
	waiting := out.String()
	for _, want := range []string{"Waiting", "Ctrl-C"} {
		if !strings.Contains(waiting, want) {
			t.Errorf("stdout = %q, want the waiting state to contain %q", waiting, want)
		}
	}
	// The command has not given up, so it must not tell the developer to run it
	// again — that is the advice a hard refusal gives, and following it here
	// would abandon the session that is waiting for them.
	if strings.Contains(waiting, "run this command again") {
		t.Errorf("stdout = %q, want a waiting command not to tell the developer to re-run it", waiting)
	}
	cancel()
	<-done
}

// TestRunDeploy_InterruptingWhileWaitingAbortsWithNothingBuilt is AC3: a
// command blocked on a browser must still answer Ctrl-C, and must not have
// built or provisioned anything when it does.
func TestRunDeploy_InterruptingWhileWaitingAbortsWithNothingBuilt(t *testing.T) {
	root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
	problemsFile(t, missingStripeKey)
	terminalStdin(t)
	var mu sync.Mutex
	var opened []string
	recordBrowser(t, &opened, &mu)
	built := false
	stubAppBuildRecorder(t, &built)

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	var out syncBuffer
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runDeploy(ctx, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
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
}

// TestRunDeploy_ClosingTheUIStillNamesTheKeysThatAreOwed: a closed browser is
// not an interrupt, and the developer who closed it has not been told what is
// missing unless the run says so on its way out.
func TestRunDeploy_ClosingTheUIStillNamesTheKeysThatAreOwed(t *testing.T) {
	root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
	problemsFile(t, missingStripeKey)
	terminalStdin(t)
	var mu sync.Mutex
	var opened []string
	recordBrowser(t, &opened, &mu)
	sessions := captureVarsUI(t)
	built := false
	stubAppBuildRecorder(t, &built)

	var out syncBuffer
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runDeploy(context.Background(), root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
	}()

	awaitVarsUI(t, &out, 1)
	// The refusal was printed once already, when the wait began. What matters
	// is what the run says on its way out, a browser session later: the
	// abandonment note alone would leave the developer with nothing to fix.
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
}

// TestRunDeploy_AReplacementThatStillFailsTheSchemaDoesNotSlipThrough is the
// re-validation AC. A UI write retracts discovery's complaint about the value
// it replaced, so a resume that only re-reads the store would deploy a second
// invalid value. Only running discovery again catches it, because the schema
// lives in the declaring process.
func TestRunDeploy_AReplacementThatStillFailsTheSchemaDoesNotSlipThrough(t *testing.T) {
	root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
	envSet(t, root, "STRIPE_API_KEY", "nope", envOptions{})
	problemsFile(t, `[{"key":"STRIPE_API_KEY","folder":"","kind":"KIND_INVALID","detail":"must start with sk_"}]`)
	terminalStdin(t)
	var mu sync.Mutex
	var opened []string
	recordBrowser(t, &opened, &mu)
	sessions := captureVarsUI(t)
	built := false
	stubAppBuildRecorder(t, &built)

	var out syncBuffer
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runDeploy(context.Background(), root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
	}()

	address, token := awaitVarsUI(t, &out, 1)
	// A second value that is just as wrong. The problems file is unchanged, so
	// the next discovery pass reports it again — and a resume that never runs
	// one would not know.
	setCell(t, address, token, "STRIPE_API_KEY", "also_nope")
	markDone(t, address, token)

	// The gate refuses again, so the UI reopens rather than the command giving
	// up: a matrix the developer called done that still does not satisfy the
	// schema is a loop, not an exit.
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
}

// TestRunDeploy_ANonInteractiveRunNeverWaits is AC5. It is today's behaviour,
// pinned so the recovery cannot quietly make a CI deploy hang on a browser
// nobody will ever open.
func TestRunDeploy_ANonInteractiveRunNeverWaits(t *testing.T) {
	root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
	t.Setenv("OCEL_TEST_ENV_PROBLEMS", missingStripeKey)
	var mu sync.Mutex
	var opened []string
	recordBrowser(t, &opened, &mu)
	built := false
	stubAppBuildRecorder(t, &built)

	// A deadline rather than a bare context: a run that wrongly waits has
	// nothing to release it, and this test's whole subject is that it does not.
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	var stdout, stderr bytes.Buffer
	err := runDeploy(ctx, root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader(""))
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
}

// TestRunDeploy_TheOptOutsKeepATerminalFromWaiting: a developer at a terminal
// over SSH is interactive by every signal the CLI has and still has no browser.
func TestRunDeploy_TheOptOutsKeepATerminalFromWaiting(t *testing.T) {
	for _, tc := range []struct {
		name string
		opts deployOptions
		env  string
	}{
		{name: "--no-ui", opts: deployOptions{yes: true, noUI: true}},
		{name: noBrowserEnvVar, opts: deployOptions{yes: true}, env: "1"},
		// Any non-empty value opts out, as OCEL_DEV does elsewhere: the
		// variable is a switch, not a boolean to be parsed.
		{name: noBrowserEnvVar + "=anything", opts: deployOptions{yes: true}, env: "true"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
			t.Setenv("OCEL_TEST_ENV_PROBLEMS", missingStripeKey)
			t.Setenv(noBrowserEnvVar, tc.env)
			terminalStdin(t)
			var mu sync.Mutex
			var opened []string
			recordBrowser(t, &opened, &mu)
			built := false
			stubAppBuildRecorder(t, &built)

			// A deadline rather than a bare context: an opt-out that stopped
			// working has nothing to release the run, and that is the subject.
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var stdout, stderr bytes.Buffer
			err := runDeploy(ctx, root, tc.opts, &stdout, &stderr, strings.NewReader(""))
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
}

// TestRunPreviewUp_AGateRefusalInATerminalOpensTheUIAndResumes: `ocel preview
// up` runs the same pre-provision path one line from `ocel deploy`'s, so it
// recovers the same way. Two commands that far apart must not behave
// differently.
func TestRunPreviewUp_AGateRefusalInATerminalOpensTheUIAndResumes(t *testing.T) {
	root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
	t.Setenv(fakeInfraClassEnvVar, "preview")
	problems := problemsFile(t, missingStripeKey)
	terminalStdin(t)
	var mu sync.Mutex
	var opened []string
	recordBrowser(t, &opened, &mu)
	built := false
	stubAppBuildRecorder(t, &built)

	var out syncBuffer
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runPreviewUp(context.Background(), root, previewUpOptions{name: "staging"}, &out, &stderr, strings.NewReader(""))
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
}

// TestRunPreviewUp_TheOptOutsAndANonTerminalKeepTheHardRefusal is AC5 for the
// command most likely to run unattended. `ocel preview up` is what a PR-preview
// job runs: a recovery that reached it regardless of the terminal would turn
// every gate-refused preview build into a job blocked on a browser nobody will
// open, until the CI timeout kills it.
func TestRunPreviewUp_TheOptOutsAndANonTerminalKeepTheHardRefusal(t *testing.T) {
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
			if tc.terminal {
				terminalStdin(t)
			}
			var mu sync.Mutex
			var opened []string
			recordBrowser(t, &opened, &mu)
			built := false
			stubAppBuildRecorder(t, &built)

			// A deadline rather than a bare context: a run that wrongly waits
			// has nothing to release it, and that is the whole subject.
			ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			var stdout, stderr bytes.Buffer
			err := runPreviewUp(ctx, root, tc.opts, &stdout, &stderr, strings.NewReader(""))
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
}

// TestRunDeploy_AReopenedUIShowsWhatIsStillOwed: the loop reopens on the gate's
// current verdict, not the one that started it. A developer who filled half the
// matrix and marked it done must not be shown the cells they already fixed.
func TestRunDeploy_AReopenedUIShowsWhatIsStillOwed(t *testing.T) {
	root := setUpEnvGateFixture(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true},{"key":"DATABASE_URL","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
	problems := problemsFile(t, `[{"key":"STRIPE_API_KEY","folder":"","kind":"KIND_MISSING"},{"key":"DATABASE_URL","folder":"","kind":"KIND_MISSING"}]`)
	terminalStdin(t)
	var mu sync.Mutex
	var opened []string
	recordBrowser(t, &opened, &mu)
	sessions := captureVarsUI(t)
	built := false
	stubAppBuildRecorder(t, &built)

	var out syncBuffer
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		done <- runDeploy(context.Background(), root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
	}()

	address, token := awaitVarsUI(t, &out, 1)
	if first := out.String(); !strings.Contains(first, "STRIPE_API_KEY") || !strings.Contains(first, "DATABASE_URL") {
		t.Fatalf("stdout = %q, want the first refusal to name both cells", first)
	}
	// Half the matrix, then done. The second discovery pass reports only what
	// is left.
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
}

// TestAbandonedRefusal_MatchesBothTheRefusalAndTheAbandonment: the exit carries
// two facts, and a caller that reaches for either — a hard-refusal branch, or
// the abandonment — must find it.
func TestAbandonedRefusal_MatchesBothTheRefusalAndTheAbandonment(t *testing.T) {
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
}

// varsUISubstrate reads which substrate the open page is addressing.
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
