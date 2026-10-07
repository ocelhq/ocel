package deploy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"maps"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/build"
	"github.com/ocelhq/ocel/cli/internal/clitest"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/exitcode"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/cli/internal/providerprocess"
	"github.com/ocelhq/ocel/cli/internal/terminal"
	"github.com/ocelhq/ocel/cli/internal/variableeditor"
	"github.com/ocelhq/ocel/cli/internal/variables"
	"github.com/ocelhq/ocel/pkg/buildoutput"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/envsource"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/statedir"
	"github.com/ocelhq/ocel/pkg/variablestore"
)

func recordBrowser(dependencies *Dependencies, opened *[]string, mu *sync.Mutex) {
	dependencies.OpenBrowser = func(url string) error {
		mu.Lock()
		defer mu.Unlock()
		*opened = append(*opened, url)
		return nil
	}
}

type editorSessions struct {
	mu  sync.Mutex
	all []*variableeditor.Session
}

func captureEditorSessions(dependencies *Dependencies) *editorSessions {
	sessions := &editorSessions{}
	prev := dependencies.ServeVariableEditor
	dependencies.ServeVariableEditor = func(ctx context.Context, cfg *project.Project, provider *providerprocess.Provider, tier environmentv1.Tier, declarations *variables.Declarations, recovery *variableeditor.Recovery) (*variableeditor.Session, error) {
		session, err := prev(ctx, cfg, provider, tier, declarations, recovery)
		if err == nil {
			sessions.mu.Lock()
			sessions.all = append(sessions.all, session)
			sessions.mu.Unlock()
		}
		return session, err
	}
	return sessions
}

func (s *editorSessions) abandon(t *testing.T, n int) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		s.mu.Lock()
		count := len(s.all)
		var session *variableeditor.Session
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

var editorURL = regexp.MustCompile(`http://127\.0\.0\.1:\d+/#t=[A-Za-z0-9_-]+`)

func awaitEditorURL(t *testing.T, out *syncBuffer, n int) (address, token string) {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if urls := editorURL.FindAllString(out.String(), -1); len(urls) >= n {
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
	clitest.WriteFile(t, path, problems)
	t.Setenv("OCEL_TEST_ENV_PROBLEMS_FILE", path)
	return path
}

func editorTier(t *testing.T, address, token string) string {
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
		Tier string `json:"tier"`
	}
	if err := json.NewDecoder(res.Body).Decode(&state); err != nil {
		t.Fatalf("decode the page's state: %v", err)
	}
	return state.Tier
}

const missingStripeKey = `[{"key":"STRIPE_API_KEY","folder":"","kind":"KIND_MISSING"}]`

func TestAMissingVariableHoldsTheRunWithTheWaitingEventAndResumesIt(t *testing.T) {
	fixture := setUpVariablesProject(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
	root := fixture.Root
	problems := problemsFile(t, missingStripeKey)
	dependencies := newTestDependencies()
	terminalStdin(&dependencies)
	useJSONFormat(t, &dependencies)
	var mu sync.Mutex
	var opened []string
	recordBrowser(&dependencies, &opened, &mu)

	var out syncBuffer
	var stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &out)
	done := make(chan error, 1)
	go func() {
		done <- runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
	}()

	address, token := awaitEditorURL(t, &out, 1)
	setCell(t, address, token, "STRIPE_API_KEY", "sk_live_filled_in")
	clitest.WriteFile(t, problems, "[]")
	markDone(t, address, token)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, out.String(), stderr.String())
		}
	case <-time.After(60 * time.Second):
		t.Fatal("runDeploy never returned after the matrix was marked done")
	}

	events := envelopes(t, out.String())
	waiting := slices.IndexFunc(events, func(event *streamv1.RunEvent) bool { return event.GetWaiting() != nil })
	if waiting < 0 {
		t.Fatalf("the run was never held for the missing variable: %s", out.String())
	}
	held := events[waiting]
	if held.GetOperation().GetPhase() != progressv1.Phase_PHASE_BUILD || !strings.HasPrefix(held.GetWaiting().GetUrl(), address) {
		t.Errorf("waiting in %s at %q, want the build held at the variables page %s", held.GetOperation().GetPhase(), held.GetWaiting().GetUrl(), address)
	}
	if missing := held.GetWaiting().GetMissing().GetCells(); len(missing) != 1 || missing[0].GetKey() != "STRIPE_API_KEY" {
		t.Errorf("waiting names %v missing, want STRIPE_API_KEY", missing)
	}
	resumed := slices.IndexFunc(events, func(event *streamv1.RunEvent) bool { return event.GetResumed() != nil })
	if resumed < waiting || !bytes.Equal(events[resumed].GetOperation().GetSpanId(), held.GetOperation().GetSpanId()) {
		t.Fatalf("resumed at event %d, waiting at %d: want the same scope resumed after the hold: %s", resumed, waiting, out.String())
	}
	built := slices.IndexFunc(events, func(event *streamv1.RunEvent) bool {
		return event.GetOperation().GetEnded() != nil && bytes.Equal(event.GetOperation().GetSpanId(), held.GetOperation().GetSpanId())
	})
	if built < resumed || events[built].GetOperation().GetEnded().GetStatus() != progressv1.SpanStatus_SPAN_STATUS_OK {
		t.Errorf("the held build ended at event %d (resumed at %d), want it to finish OK after it resumed", built, resumed)
	}
}

func TestADeployMissingVariablesOpensTheEditorAndResumesOnceTheyAreSet(t *testing.T) {
	t.Run("a declarations refusal in a terminal opens the UI and resumes into the build", func(t *testing.T) {
		fixture := setUpVariablesProject(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		root := fixture.Root
		problems := problemsFile(t, missingStripeKey)
		dependencies := newTestDependencies()
		terminalStdin(&dependencies)
		var mu sync.Mutex
		var opened []string
		recordBrowser(&dependencies, &opened, &mu)

		built := false
		stubAppBuildRecorder(&dependencies, &built)

		var out syncBuffer
		var stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			clitest.AttachTerminalSink(dependencies.Invocation, &out)
			done <- runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
		}()

		address, token := awaitEditorURL(t, &out, 1)
		setCell(t, address, token, "STRIPE_API_KEY", "sk_live_filled_in")
		clitest.WriteFile(t, problems, "[]")
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
		fixture := setUpVariablesProject(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
		root := fixture.Root
		writeRootApp(t, root)
		problems := problemsFile(t, missingStripeKey)
		dependencies := newTestDependencies()
		terminalStdin(&dependencies)
		var mu sync.Mutex
		var opened []string
		recordBrowser(&dependencies, &opened, &mu)
		stubBuild(&dependencies, []build.Function{
			{Route: "api", Framework: buildoutput.Framework{Name: "node"}, EntryFile: "src/server.js", ArtifactPath: "output/api", App: clitest.FixtureSlug},
		})

		var out syncBuffer
		var stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			clitest.AttachTerminalSink(dependencies.Invocation, &out)
			done <- runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
		}()

		address, token := awaitEditorURL(t, &out, 1)
		setCell(t, address, token, "STRIPE_API_KEY", "pk_filled_in")
		clitest.WriteFile(t, problems, "[]")
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
		declared := 0
		for _, variable := range manifestApp(t, sentDeploy(t, fixture).GetManifest(), clitest.FixtureSlug).GetVariables() {
			if variable.GetKey() == "STRIPE_API_KEY" {
				declared++
			}
		}
		if declared != 1 {
			t.Errorf("the resumed manifest declares STRIPE_API_KEY %d times, want exactly once", declared)
		}
		if strings.Contains(got, "pk_filled_in") {
			t.Errorf("stdout = %q, want no variable value ever printed", got)
		}
	})

	t.Run("the waiting state says how to abort", func(t *testing.T) {
		fixture := setUpVariablesProject(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		root := fixture.Root
		problemsFile(t, missingStripeKey)
		dependencies := newTestDependencies()
		terminalStdin(&dependencies)
		var mu sync.Mutex
		var opened []string
		recordBrowser(&dependencies, &opened, &mu)
		built := false
		stubAppBuildRecorder(&dependencies, &built)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var out syncBuffer
		var stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			clitest.AttachTerminalSink(dependencies.Invocation, &out)
			done <- runDeploy(ctx, dependencies, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
		}()

		awaitEditorURL(t, &out, 1)
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
		fixture := setUpVariablesProject(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		root := fixture.Root
		problemsFile(t, missingStripeKey)
		dependencies := newTestDependencies()
		terminalStdin(&dependencies)
		var mu sync.Mutex
		var opened []string
		recordBrowser(&dependencies, &opened, &mu)
		built := false
		stubAppBuildRecorder(&dependencies, &built)

		ctx, cancel := context.WithCancel(context.Background())
		defer cancel()
		var out syncBuffer
		var stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			clitest.AttachTerminalSink(dependencies.Invocation, &out)
			done <- runDeploy(ctx, dependencies, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
		}()

		awaitEditorURL(t, &out, 1)
		cancel()

		select {
		case err := <-done:
			var exit *exitcode.ExitError
			if !errors.As(err, &exit) || exit.Code == 0 {
				t.Fatalf("runDeploy err = %v, want a non-zero exit; stdout=%s", err, out.String())
			}
		case <-time.After(60 * time.Second):
			t.Fatal("runDeploy never returned after the context was cancelled")
		}

		if built {
			t.Error("the app was built, want an interrupted wait to build nothing")
		}
		if sent := sentDeploys(t, fixture); len(sent) != 0 {
			t.Errorf("the CLI sent %d deploys, want none after an interrupted wait", len(sent))
		}
		if strings.Contains(out.String(), "Resources may be partially created") {
			t.Errorf("stdout = %q, want an interrupted wait to say nothing was provisioned", out.String())
		}
	})

	t.Run("closing the UI still names the keys that are missing", func(t *testing.T) {
		fixture := setUpVariablesProject(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		root := fixture.Root
		problemsFile(t, missingStripeKey)
		dependencies := newTestDependencies()
		terminalStdin(&dependencies)
		var mu sync.Mutex
		var opened []string
		recordBrowser(&dependencies, &opened, &mu)
		sessions := captureEditorSessions(&dependencies)
		built := false
		stubAppBuildRecorder(&dependencies, &built)

		var out syncBuffer
		var stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			clitest.AttachTerminalSink(dependencies.Invocation, &out)
			done <- runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
		}()

		awaitEditorURL(t, &out, 1)
		before := out.String()
		sessions.abandon(t, 1)

		select {
		case err := <-done:
			var exit *exitcode.ExitError
			if !errors.As(err, &exit) || exit.Code == 0 {
				t.Fatalf("runDeploy err = %v, want a non-zero exit", err)
			}
		case <-time.After(60 * time.Second):
			t.Fatal("runDeploy never returned after the UI was abandoned")
		}

		tail := strings.TrimPrefix(out.String(), before) + stderr.String()
		for _, want := range []string{"STRIPE_API_KEY", "Fill them in: ocel env ui", "closed before the matrix was complete"} {
			if !strings.Contains(tail, want) {
				t.Errorf("output after the UI closed = %q, want it to contain %q", tail, want)
			}
		}
		if built {
			t.Error("the app was built, want an abandoned wait to build nothing")
		}
	})

	t.Run("a replacement that still fails the schema fails the deploy without reopening the UI", func(t *testing.T) {
		fixture := setUpVariablesProject(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		root := fixture.Root
		envSet(t, fixture, "STRIPE_API_KEY", "nope", envOptions{})
		problemsFile(t, `[{"key":"STRIPE_API_KEY","folder":"","kind":"KIND_INVALID","detail":"must start with sk_"}]`)
		dependencies := newTestDependencies()
		terminalStdin(&dependencies)
		var mu sync.Mutex
		var opened []string
		recordBrowser(&dependencies, &opened, &mu)
		built := false
		stubAppBuildRecorder(&dependencies, &built)

		var out syncBuffer
		var stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			clitest.AttachTerminalSink(dependencies.Invocation, &out)
			done <- runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
		}()

		address, token := awaitEditorURL(t, &out, 1)
		setCell(t, address, token, "STRIPE_API_KEY", "also_nope")
		markDone(t, address, token)

		select {
		case err := <-done:
			var exit *exitcode.ExitError
			if !errors.As(err, &exit) || exit.Code == 0 {
				t.Fatalf("runDeploy err = %v, want the second invalid value refused with a non-zero exit; stdout=%s", err, out.String())
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
		if urls := editorURL.FindAllString(out.String(), -1); len(urls) != 1 {
			t.Errorf("the UI was offered %d times, want once per deploy: %q", len(urls), out.String())
		}
		mu.Lock()
		defer mu.Unlock()
		if len(opened) != 1 {
			t.Errorf("opened = %v, want the browser launched exactly once", opened)
		}
		if strings.Contains(out.String(), "also_nope") || strings.Contains(out.String(), "nope") {
			t.Errorf("stdout = %q, want no variable value ever printed", out.String())
		}
	})

	t.Run("returning with a cell still missing is refused, and abandoning fails the deploy once", func(t *testing.T) {
		fixture := setUpVariablesProject(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true},{"key":"DATABASE_URL","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		root := fixture.Root
		problemsFile(t, `[{"key":"STRIPE_API_KEY","folder":"","kind":"KIND_MISSING"},{"key":"DATABASE_URL","folder":"","kind":"KIND_MISSING"}]`)
		dependencies := newTestDependencies()
		terminalStdin(&dependencies)
		var mu sync.Mutex
		var opened []string
		recordBrowser(&dependencies, &opened, &mu)
		built := false
		stubAppBuildRecorder(&dependencies, &built)

		var out syncBuffer
		var stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			clitest.AttachTerminalSink(dependencies.Invocation, &out)
			done <- runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
		}()

		address, token := awaitEditorURL(t, &out, 1)
		if first := out.String(); !strings.Contains(first, "STRIPE_API_KEY") || !strings.Contains(first, "DATABASE_URL") {
			t.Fatalf("stdout = %q, want the first refusal to name both cells", first)
		}
		setCell(t, address, token, "STRIPE_API_KEY", "sk_live_filled_in")

		req, err := http.NewRequest(http.MethodPost, address+"/api/done", nil)
		if err != nil {
			t.Fatalf("build the done request: %v", err)
		}
		req.Header.Set("Authorization", "Bearer "+token)
		res, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatalf("mark the matrix done: %v", err)
		}
		refusal, _ := io.ReadAll(res.Body)
		res.Body.Close()
		if res.StatusCode != http.StatusConflict || !strings.Contains(string(refusal), "DATABASE_URL") {
			t.Fatalf("done with a cell missing = %d %q, want %d naming DATABASE_URL", res.StatusCode, refusal, http.StatusConflict)
		}
		if strings.Contains(string(refusal), "STRIPE_API_KEY") {
			t.Errorf("refusal = %q, want a cell the developer already filled not shown as missing", refusal)
		}

		before := out.String()
		abandon, err := http.NewRequest(http.MethodPost, address+"/api/abandon", nil)
		if err != nil {
			t.Fatalf("build the abandon request: %v", err)
		}
		abandon.Header.Set("Authorization", "Bearer "+token)
		if res, err := http.DefaultClient.Do(abandon); err != nil {
			t.Fatalf("abandon: %v", err)
		} else {
			res.Body.Close()
		}

		select {
		case err := <-done:
			var exit *exitcode.ExitError
			if !errors.As(err, &exit) || exit.Code == 0 {
				t.Fatalf("runDeploy err = %v, want the abandoned deploy to exit non-zero", err)
			}
		case <-time.After(60 * time.Second):
			t.Fatal("runDeploy never returned")
		}
		tail := strings.TrimPrefix(out.String(), before) + stderr.String()
		if !strings.Contains(tail, "DATABASE_URL") || !strings.Contains(tail, "closed before the matrix was complete") {
			t.Errorf("output after abandoning = %q, want the missing cell and the abandonment named", tail)
		}
		if urls := editorURL.FindAllString(out.String(), -1); len(urls) != 1 {
			t.Errorf("the UI was offered %d times, want once per deploy", len(urls))
		}
		mu.Lock()
		defer mu.Unlock()
		if len(opened) != 1 {
			t.Errorf("opened = %v, want the browser launched exactly once", opened)
		}
		if built {
			t.Error("the app was built with a required variable still missing")
		}
	})

	t.Run("interactivity alone decides whether a variables requirement can pause", func(t *testing.T) {
		for _, tc := range []struct {
			name     string
			terminal bool
			opts     deployOptions
			env      string
		}{
			{name: "off a terminal", opts: deployOptions{}},
			{name: "off a terminal with --yes", opts: deployOptions{yes: true}},
			{name: commands.NoBrowserEnvVar, terminal: true, opts: deployOptions{}, env: "1"},
			{name: commands.NoBrowserEnvVar + "=anything", terminal: true, opts: deployOptions{yes: true}, env: "true"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fixture := setUpVariablesProject(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
				root := fixture.Root
				t.Setenv("OCEL_TEST_ENV_PROBLEMS", missingStripeKey)
				t.Setenv(commands.NoBrowserEnvVar, tc.env)
				dependencies := newTestDependencies()
				if tc.terminal {
					terminalStdin(&dependencies)
				}
				var mu sync.Mutex
				var opened []string
				recordBrowser(&dependencies, &opened, &mu)
				built := false
				stubAppBuildRecorder(&dependencies, &built)

				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				var stdout, stderr bytes.Buffer
				clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
				err := runDeploy(ctx, dependencies, root, tc.opts, &stdout, &stderr, strings.NewReader(""))
				if err == nil {
					t.Fatal("runDeploy err = nil, want the variables requirement to be terminal")
				}
				for _, want := range []string{"STRIPE_API_KEY", "ocel env set STRIPE_API_KEY"} {
					if !strings.Contains(stdout.String(), want) {
						t.Errorf("stdout = %q, want the refusal to list the missing variable (%q)", stdout.String(), want)
					}
				}
				if editorURL.MatchString(stdout.String()) {
					t.Errorf("stdout = %q, want no variables UI opened", stdout.String())
				}
				if built {
					t.Error("the app was built, want the declarations to refuse before any build runs")
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

func TestAPreviewMissingVariablesOpensTheEditorAndResumesOnceTheyAreSet(t *testing.T) {
	t.Run("a declarations refusal in a terminal opens the UI and resumes", func(t *testing.T) {
		fixture := setUpVariablesProject(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
		root := fixture.Root
		bootstrapTier(t, fixture, environment.TierPreview)
		problems := problemsFile(t, missingStripeKey)
		dependencies := newTestDependencies()
		terminalStdin(&dependencies)
		var mu sync.Mutex
		var opened []string
		recordBrowser(&dependencies, &opened, &mu)
		built := false
		stubAppBuildRecorder(&dependencies, &built)

		var out syncBuffer
		var stderr bytes.Buffer
		done := make(chan error, 1)
		go func() {
			clitest.AttachTerminalSink(dependencies.Invocation, &out)
			done <- runPreviewUp(context.Background(), dependencies, root, previewUpOptions{name: "staging", persistent: true}, &out, &stderr, strings.NewReader(""))
		}()

		address, token := awaitEditorURL(t, &out, 1)
		if got := editorTier(t, address, token); got != "preview" {
			t.Errorf("bootstrap = %q, want the preview's own", got)
		}
		setCell(t, address, token, "STRIPE_API_KEY", "sk_live_filled_in")
		clitest.WriteFile(t, problems, "[]")
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
			{name: "no terminal", opts: previewUpOptions{name: "staging", persistent: true}},
			{name: "no terminal with --yes", opts: previewUpOptions{name: "staging", persistent: true, yes: true}},
		} {
			t.Run(tc.name, func(t *testing.T) {
				fixture := setUpVariablesProject(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
				root := fixture.Root
				bootstrapTier(t, fixture, environment.TierPreview)
				t.Setenv("OCEL_TEST_ENV_PROBLEMS", missingStripeKey)
				dependencies := newTestDependencies()
				if tc.terminal {
					terminalStdin(&dependencies)
				}
				var mu sync.Mutex
				var opened []string
				recordBrowser(&dependencies, &opened, &mu)
				built := false
				stubAppBuildRecorder(&dependencies, &built)

				ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
				defer cancel()
				var stdout, stderr bytes.Buffer
				clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
				err := runPreviewUp(ctx, dependencies, root, tc.opts, &stdout, &stderr, strings.NewReader(""))
				if err == nil {
					t.Fatal("runPreviewUp err = nil, want the hard refusal kept")
				}
				if editorURL.MatchString(stdout.String()) {
					t.Errorf("stdout = %q, want no variables UI opened", stdout.String())
				}
				if built {
					t.Error("the app was built, want the declarations to refuse before any build runs")
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

func TestAnAbandonedRecoveryIsBothTheRefusalAndTheAbandonment(t *testing.T) {
	t.Parallel()

	t.Run("matches both the refusal and the abandonment", func(t *testing.T) {
		t.Parallel()

		refusal := &variables.MissingError{Problems: []*resourcesv1.VariableProblem{
			{Key: "STRIPE_API_KEY", Kind: resourcesv1.VariableProblem_KIND_MISSING},
		}}
		var err error = &abandonedRefusal{refusal: refusal}

		if !errors.Is(err, variableeditor.ErrAbandoned) {
			t.Error("errors.Is(err, ErrAbandoned) = false, want an abandonment the caller can match")
		}
		var got *variables.MissingError
		if !errors.As(err, &got) || got != refusal {
			t.Error("errors.As(err, *variables.MissingError) did not recover the original refusal")
		}
		if !strings.Contains(err.Error(), "STRIPE_API_KEY") {
			t.Errorf("err = %q, want the keys that are missing named", err)
		}
		var abandoned *abandonedRefusal
		if !errors.As(err, &abandoned) {
			t.Fatalf("errors.As(err, *abandonedRefusal) = false for %v", err)
		}
		if got, want := abandoned.Detail(), variableeditor.AbandonedMessage+"."; got != want {
			t.Errorf("Detail = %q, want %q beyond the missing variables", got, want)
		}
	})
}

type syncBuffer struct {
	mu  sync.Mutex
	buf bytes.Buffer
}

func (b *syncBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.Write(p)
}

func (b *syncBuffer) String() string {
	b.mu.Lock()
	defer b.mu.Unlock()
	return b.buf.String()
}

type traceSpan struct {
	Name              string      `json:"name"`
	SpanID            string      `json:"spanId"`
	ParentSpanID      string      `json:"parentSpanId"`
	StartTimeUnixNano string      `json:"startTimeUnixNano"`
	EndTimeUnixNano   string      `json:"endTimeUnixNano"`
	Attributes        []traceAttr `json:"attributes"`
}

type traceAttr struct {
	Key   string `json:"key"`
	Value struct {
		IntValue *string `json:"intValue"`
	} `json:"value"`
}

func (s traceSpan) retryCount(t *testing.T) int {
	t.Helper()
	for _, a := range s.Attributes {
		if a.Key == "ocel.retry_count" && a.Value.IntValue != nil {
			n, err := strconv.Atoi(*a.Value.IntValue)
			if err != nil {
				t.Fatalf("parse retry_count %q: %v", *a.Value.IntValue, err)
			}
			return n
		}
	}
	t.Fatalf("span %q has no ocel.retry_count attribute", s.Name)
	return -1
}

func (s traceSpan) durationNS(t *testing.T) int64 {
	t.Helper()
	return parseNano(t, s.EndTimeUnixNano) - parseNano(t, s.StartTimeUnixNano)
}

func parseNano(t *testing.T, s string) int64 {
	t.Helper()
	n, err := strconv.ParseInt(s, 10, 64)
	if err != nil {
		t.Fatalf("timestamp %q is not a plain integer: %v", s, err)
	}
	return n
}

func readTraceSpans(t *testing.T, root string) []traceSpan {
	t.Helper()
	matches, err := filepath.Glob(filepath.Join(root, statedir.Name, "runs", "*.otlp.json"))
	if err != nil {
		t.Fatalf("glob trace files: %v", err)
	}
	if len(matches) != 1 {
		t.Fatalf("trace files under %s = %v, want exactly one", root, matches)
	}
	var doc struct {
		ResourceSpans []struct {
			ScopeSpans []struct {
				Spans []traceSpan `json:"spans"`
			} `json:"scopeSpans"`
		} `json:"resourceSpans"`
	}
	raw, err := os.ReadFile(matches[0])
	if err != nil {
		t.Fatalf("read trace file: %v", err)
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("trace file is not valid OTLP/JSON: %v", err)
	}
	var spans []traceSpan
	for _, rs := range doc.ResourceSpans {
		for _, ss := range rs.ScopeSpans {
			spans = append(spans, ss.Spans...)
		}
	}
	return spans
}

func spansNamed(spans []traceSpan, name string) []traceSpan {
	var out []traceSpan
	for _, s := range spans {
		if s.Name == name {
			out = append(out, s)
		}
	}
	return out
}

func rootSpan(t *testing.T, spans []traceSpan) traceSpan {
	t.Helper()
	for _, s := range spans {
		if s.ParentSpanID == "" {
			return s
		}
	}
	t.Fatal("no root span (a span with no parent) in the trace file")
	return traceSpan{}
}

func TestVariablesRecoveryTracesEachAttemptAndTheHumanWait(t *testing.T) {
	fixture := setUpVariablesProject(t, `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`)
	root := fixture.Root
	problems := problemsFile(t, missingStripeKey)
	dependencies := newTestDependencies()
	terminalStdin(&dependencies)
	var mu sync.Mutex
	var opened []string
	recordBrowser(&dependencies, &opened, &mu)

	var out syncBuffer
	var stderr bytes.Buffer
	done := make(chan error, 1)
	go func() {
		clitest.AttachTerminalSink(dependencies.Invocation, &out)
		done <- runDeploy(context.Background(), dependencies, root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
	}()

	address, token := awaitEditorURL(t, &out, 1)
	setCell(t, address, token, "STRIPE_API_KEY", "sk_live_filled_in")
	clitest.WriteFile(t, problems, "[]")
	markDone(t, address, token)

	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, out.String(), stderr.String())
		}
	case <-time.After(60 * time.Second):
		t.Fatal("runDeploy never returned after the matrix was marked done")
	}

	spans := readTraceSpans(t, root)

	builds := spansNamed(spans, "build")
	if len(builds) != 2 {
		t.Fatalf("got %d spans named %q, want 2 (the refused attempt and the resumed one)", len(builds), "build")
	}
	seen := map[int]bool{}
	for _, b := range builds {
		seen[b.retryCount(t)] = true
	}
	if !seen[0] || !seen[1] {
		t.Errorf("build span retry_count values = %v, want 0 and 1", seen)
	}

	waits := spansNamed(spans, "await_human_input")
	if len(waits) != 1 {
		t.Fatalf("got %d spans named %q, want exactly 1", len(waits), "await_human_input")
	}

	root0 := rootSpan(t, spans)
	collecting := builds[0].ParentSpanID
	for _, s := range append(append([]traceSpan{}, builds...), waits[0]) {
		if s.ParentSpanID != collecting || s.ParentSpanID == root0.SpanID {
			t.Errorf("span %q parent = %q, want the span %q that collects the resources — a sibling of the build attempts, not nested in one", s.Name, s.ParentSpanID, collecting)
		}
	}

	var buildSum int64
	for _, b := range builds {
		buildSum += b.durationNS(t)
	}
	waitDuration := waits[0].durationNS(t)
	rootDuration := root0.durationNS(t)
	if rootDuration < buildSum+waitDuration {
		t.Errorf("root span duration = %dns, want it to cover both build attempts (%dns) and the wait (%dns)", rootDuration, buildSum, waitDuration)
	}
}

const stripeRequired = `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true}]`

func TestMissingVariablesUnderJSONOnATerminalFailWithTheirRemedyAndWaitForNobody(t *testing.T) {
	for _, tc := range []struct {
		name string
		run  func(ctx context.Context, t *testing.T, dependencies Dependencies, fixture clitest.FakeProject, stdout io.Writer, stdin io.Reader) error
		set  func(t *testing.T) (clitest.FakeProject, Dependencies)
	}{
		{
			name: "ocel deploy",
			set: func(t *testing.T) (clitest.FakeProject, Dependencies) {
				fixture := setUpVariablesProject(t, stripeRequired)
				t.Setenv("OCEL_TEST_ENV_PROBLEMS", missingStripeKey)
				return fixture, newTestDependencies()
			},
			run: func(ctx context.Context, t *testing.T, dependencies Dependencies, fixture clitest.FakeProject, stdout io.Writer, stdin io.Reader) error {
				return runDeploy(ctx, dependencies, fixture.Root, deployOptions{yes: true}, stdout, stdout, stdin)
			},
		},
		{
			name: "ocel preview up",
			set: func(t *testing.T) (clitest.FakeProject, Dependencies) {
				fixture, dependencies := setUpPreviewVariablesProject(t, stripeRequired)
				t.Setenv("OCEL_TEST_ENV_PROBLEMS", missingStripeKey)
				return fixture, dependencies
			},
			run: func(ctx context.Context, t *testing.T, dependencies Dependencies, fixture clitest.FakeProject, stdout io.Writer, stdin io.Reader) error {
				return runPreviewUp(ctx, dependencies, fixture.Root, previewUpOptions{name: "staging", persistent: true, yes: true}, stdout, stdout, stdin)
			},
		},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Setenv(commands.NoBrowserEnvVar, "")
			fixture, dependencies := tc.set(t)
			var mu sync.Mutex
			var opened []string
			recordBrowser(&dependencies, &opened, &mu)
			tty, screen := clitest.UnderJSONOnATerminal(t, &dependencies.Invocation)
			var stdout syncBuffer
			dependencies.Events.Attach(terminal.NewJSONLines(&stdout))

			err := clitest.FinishWithin(t, 20*time.Second, func(ctx context.Context) error {
				return tc.run(ctx, t, dependencies, fixture, &stdout, tty)
			})

			if err == nil {
				t.Fatal("err = nil, want the missing variable refused")
			}
			if asked := screen(); asked != "" {
				t.Errorf("terminal = %q, want nothing asked under --json", asked)
			}
			evs := clitest.RunEvents(t, stdout.String())
			got := evs[len(evs)-1].GetSummary().GetError()
			if got.GetCode() != "variables.missing" || !strings.HasPrefix(got.GetHint(), "ocel env set STRIPE_API_KEY=<VALUE>") {
				t.Errorf("summary error = %v, want variables.missing hinting ocel env set", got)
			}
			if slices.ContainsFunc(evs, func(ev *streamv1.RunEvent) bool { return ev.GetWaiting() != nil }) {
				t.Errorf("the run was held for a person under --json: %s", stdout.String())
			}
			mu.Lock()
			defer mu.Unlock()
			if len(opened) != 0 {
				t.Errorf("opened %v in a browser, want nothing opened under --json", opened)
			}
		})
	}
}

func TestADeployIsRefusedUntilItsVariablesAreReady(t *testing.T) {
	t.Run("a missing value refuses before anything is built", func(t *testing.T) {
		fixture := setUpVariablesProject(t, stripeRequired)
		t.Setenv("OCEL_TEST_ENV_PROBLEMS", missingStripeKey)
		dependencies := newTestDependencies()
		built := false
		stubAppBuildRecorder(&dependencies, &built)

		out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
		if err == nil {
			t.Fatal("runDeploy err = nil, want the declarations to refuse")
		}
		var exit *exitcode.ExitError
		if !errors.As(err, &exit) || exit.Code == 0 {
			t.Errorf("runDeploy err = %v, want a non-zero exit", err)
		}
		for _, want := range []string{"STRIPE_API_KEY", "ocel env set STRIPE_API_KEY=<VALUE>"} {
			if !strings.Contains(out, want) {
				t.Errorf("output = %q, want it to contain %q", out, want)
			}
		}
		if built {
			t.Error("the app was built, want the declarations to refuse before any build runs")
		}
		if sent := sentDeploys(t, fixture); len(sent) != 0 {
			t.Errorf("the CLI sent %d deploys, want none", len(sent))
		}
	})

	t.Run("a client value that fails its schema refuses before anything is built, naming the key and the complaint", func(t *testing.T) {
		fixture := setUpVariablesProject(t, `[{"key":"NEXT_PUBLIC_PORT","class":"VARIABLE_CLASS_PLAIN","required":true,"clientAccessible":true,"hasSchema":true,"schemaSource":"/app/env.schema.ts","source":"/app/env.ts"}]`)
		t.Setenv("OCEL_TEST_ENV_PROBLEMS", `[{"key":"NEXT_PUBLIC_PORT","folder":"","kind":"KIND_INVALID","detail":"expected a number"}]`)
		dependencies := newTestDependencies()
		built := false
		stubAppBuildRecorder(&dependencies, &built)

		out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
		if err == nil {
			t.Fatal("runDeploy err = nil, want the declarations to refuse")
		}
		for _, want := range []string{"NEXT_PUBLIC_PORT", "set, but expected a number", "ocel env set NEXT_PUBLIC_PORT=<VALUE>"} {
			if !strings.Contains(out, want) {
				t.Errorf("output = %q, want it to contain %q", out, want)
			}
		}
		if built {
			t.Error("the app was built with a client value its schema rejects")
		}
	})

	t.Run("a missing value refuses though discovery reported nothing", func(t *testing.T) {
		fixture := setUpVariablesProjectWith(t, stripeRequired, clitest.EnvDeclareOnlyScript)
		dependencies := newTestDependencies()
		built := false
		stubAppBuildRecorder(&dependencies, &built)

		out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
		if err == nil {
			t.Fatal("runDeploy err = nil, want the declarations to refuse on what it knows itself")
		}
		for _, want := range []string{"STRIPE_API_KEY", "ocel env set STRIPE_API_KEY=<VALUE>"} {
			if !strings.Contains(out, want) {
				t.Errorf("output = %q, want it to contain %q", out, want)
			}
		}
		if built {
			t.Error("the app was built, want the declarations to refuse before any build runs")
		}
	})

	t.Run("a value that is set passes the declarations and deploys", func(t *testing.T) {
		fixture := setUpVariablesProject(t, stripeRequired)
		envSet(t, fixture, "STRIPE_API_KEY", "sk_live_value", envOptions{})

		out, err := deployWith(t, newTestDependencies(), fixture, deployOptions{yes: true})
		if err != nil {
			t.Fatalf("runDeploy err = %v; output=%s", err, out)
		}
		if !strings.Contains(out, "Deployed") {
			t.Errorf("output = %q, want the deploy to have completed", out)
		}
	})

	t.Run("a value that cannot be read names the cell", func(t *testing.T) {
		fixture := setUpVariablesProject(t, stripeRequired)
		envSet(t, fixture, "STRIPE_API_KEY", "sk_live_value", envOptions{})
		fixture.Provider.Cipher().(*fake.Cipher).RefuseOpening(errors.New("the store is unreachable"))

		out, err := deployWith(t, newTestDependencies(), fixture, deployOptions{yes: true})
		if err == nil {
			t.Fatal("runDeploy err = nil, want a store it cannot read to stop the deploy")
		}
		if said := out + err.Error(); !strings.Contains(said, "STRIPE_API_KEY (project root)") {
			t.Errorf("output = %q, want it to name the cell that could not be read", said)
		}
	})

	t.Run("a live value is never handed to the declaring process", func(t *testing.T) {
		fixture := setUpVariablesProject(t, `[{"key":"LIVE_KEY","class":"VARIABLE_CLASS_SECRET","required":true},{"key":"BAKED_KEY","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
		envSet(t, fixture, "LIVE_KEY", "sk_live_do_not_leak", envOptions{})
		envSet(t, fixture, "BAKED_KEY", "baked_value", envOptions{})

		cellsPath := filepath.Join(t.TempDir(), "cells.json")
		t.Setenv("OCEL_TEST_ENV_CELLS_OUT", cellsPath)

		if out, err := deployWith(t, newTestDependencies(), fixture, deployOptions{yes: true}); err != nil {
			t.Fatalf("runDeploy err = %v; output=%s", err, out)
		}

		raw, readErr := os.ReadFile(cellsPath)
		if readErr != nil {
			t.Fatalf("read cells handed to discovery: %v", readErr)
		}
		if strings.Contains(string(raw), "sk_live_do_not_leak") {
			t.Errorf("cells = %s, want a live value never pulled onto the build host", raw)
		}

		var cells []struct {
			Key   string `json:"key"`
			Value string `json:"value"`
		}
		if err := json.Unmarshal(raw, &cells); err != nil {
			t.Fatalf("unmarshal cells: %v", err)
		}
		byKey := map[string]string{}
		for _, c := range cells {
			byKey[c.Key] = c.Value
		}
		if _, ok := byKey["LIVE_KEY"]; !ok {
			t.Error("cells has no LIVE_KEY, want the live cell reported present so it is not called missing")
		}
		if byKey["BAKED_KEY"] != "baked_value" {
			t.Errorf("BAKED_KEY = %q, want the plaintext its schema is checked against", byKey["BAKED_KEY"])
		}
	})

	t.Run("a folder no app binds is a warning, not a refusal", func(t *testing.T) {
		fixture := setUpVariablesProject(t, `[{"key":"PAGE_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web"]}]`)
		writeAppsConfig(t, fixture.Root, `{ name: "api", path: "apps/api", framework: "node" }`)
		writeAppSource(t, fixture.Root, "api")
		envSet(t, fixture, "PAGE_ID", "page_web", envOptions{folder: "/web"})
		dependencies := newTestDependencies()
		stubBuild(&dependencies, nil)

		out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
		if err != nil {
			t.Fatalf("runDeploy err = %v, want a dead scope to warn, not stop the deploy; output=%s", err, out)
		}
		if !strings.Contains(out, "PAGE_ID") || !strings.Contains(out, "/web") {
			t.Errorf("output = %q, want a warning naming the key and the folder no app binds", out)
		}
	})

	t.Run("each app is built with its own diverged value", func(t *testing.T) {
		fixture := setUpVariablesProject(t, `[{"key":"PAGE_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web","/admin"]}]`)
		writeAppsConfig(t, fixture.Root, `
    { name: "web", path: "apps/web", framework: "node", folder: "/web" },
    { name: "admin", path: "apps/admin", framework: "node", folder: "/admin" }`)
		writeAppSource(t, fixture.Root, "web", "admin")
		envSet(t, fixture, "PAGE_ID", "page_web", envOptions{folder: "/web"})
		envSet(t, fixture, "PAGE_ID", "page_admin", envOptions{folder: "/admin"})

		dependencies := newTestDependencies()
		got := captureBuildVariables(&dependencies)

		if out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true}); err != nil {
			t.Fatalf("runDeploy err = %v; output=%s", err, out)
		}
		if (*got)["web"].Env["PAGE_ID"] != "page_web" || (*got)["admin"].Env["PAGE_ID"] != "page_admin" {
			t.Errorf("build environments = %v, want each app the value it resolved", *got)
		}
	})

	t.Run("a half-completed folder rename stops the deploy naming both files", func(t *testing.T) {
		fixture := setUpVariablesProject(t, `[{"key":"PAGE_ID","class":"VARIABLE_CLASS_PLAIN","required":true,"folders":["/web","/admin"],"source":"ocel/env.ts"}]`)
		writeAppsConfig(t, fixture.Root, `
    { name: "web", path: "apps/web", framework: "node", folder: "/web" },
    { name: "admin", path: "apps/admin", framework: "node", folder: "/administration" }`)
		envSet(t, fixture, "PAGE_ID", "page_web", envOptions{folder: "/web"})
		envSet(t, fixture, "PAGE_ID", "page_admin", envOptions{folder: "/admin"})

		dependencies := newTestDependencies()
		built := false
		stubAppBuildRecorder(&dependencies, &built)

		out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
		if err == nil {
			t.Fatal("runDeploy err = nil, want a half-finished folder rename to stop the deploy")
		}
		said := out + err.Error()
		for _, want := range []string{"PAGE_ID", "/admin", "ocel.config.ts", "env.ts"} {
			if !strings.Contains(said, want) {
				t.Errorf("output = %q, want it to name %q", said, want)
			}
		}
		if built {
			t.Error("the app was built, want the lint to refuse before any build runs")
		}
	})

	t.Run("a reference satisfies the declarations with its source's value", func(t *testing.T) {
		fixture := setUpVariablesProject(t, `[{"key":"PAGE_ID","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
		ownedElsewhere(t, fixture, "platform", "PAGE_ID", "page_owned_by_platform")
		envRef(t, fixture, "PAGE_ID", "platform")
		writeRootApp(t, fixture.Root)

		dependencies := newTestDependencies()
		got := captureBuildVariables(&dependencies)

		if out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true}); err != nil {
			t.Fatalf("runDeploy err = %v; output=%s", err, out)
		}
		if (*got)[clitest.FixtureSlug].Env["PAGE_ID"] != "page_owned_by_platform" {
			t.Errorf("build environment = %v, want the value the other project stores", *got)
		}
	})
}

func setUpPreviewVariablesProject(t *testing.T, definitions string) (clitest.FakeProject, Dependencies) {
	t.Helper()
	fixture := setUpVariablesProject(t, definitions)
	bootstrapTier(t, fixture, environment.TierPreview)
	writeRootApp(t, fixture.Root)
	dependencies := newTestDependencies()
	stubGit(&dependencies, "feature/login", "")
	return fixture, dependencies
}

func previewUpWith(t *testing.T, fixture clitest.FakeProject, dependencies Dependencies, opts previewUpOptions) (string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	err := runPreviewUp(context.Background(), dependencies, fixture.Root, opts, &stdout, &stderr, strings.NewReader(""))
	return stdout.String() + stderr.String(), err
}

func TestAPreviewIsRefusedUntilItsVariablesAreReady(t *testing.T) {
	t.Run("a production value does not satisfy the preview declarations", func(t *testing.T) {
		fixture, dependencies := setUpPreviewVariablesProject(t, stripeRequired)
		envSet(t, fixture, "STRIPE_API_KEY", "sk_live_secret", envOptions{})
		built := false
		stubAppBuildRecorder(&dependencies, &built)

		out, err := previewUpWith(t, fixture, dependencies, previewUpOptions{name: "staging", persistent: true})
		if err == nil {
			t.Fatal("runPreviewUp err = nil, want the preview declarations to refuse: the production store is not the preview one")
		}
		said := out + err.Error()
		if !strings.Contains(said, "STRIPE_API_KEY") {
			t.Errorf("output = %q, want it to name the cell the preview bootstrap is missing", said)
		}
		if strings.Contains(said, "sk_live_secret") {
			t.Errorf("output = %q, want no production value reachable from a preview", said)
		}
		if built {
			t.Error("the app was built, want the declarations to refuse before any build runs")
		}
	})

	t.Run("the environment being deployed resolves its own override", func(t *testing.T) {
		for name, tc := range map[string]struct {
			deploying string
			want      string
		}{
			"the environment with the override": {deploying: "staging", want: "page_staging"},
			"another preview":                   {deploying: "canary", want: "page_shared"},
		} {
			t.Run(name, func(t *testing.T) {
				fixture, dependencies := setUpPreviewVariablesProject(t, `[{"key":"PAGE_ID","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
				envSet(t, fixture, "PAGE_ID", "page_shared", envOptions{preview: true})
				envSet(t, fixture, "PAGE_ID", "page_staging", envOptions{preview: true, environment: "staging"})
				got := captureBuildVariables(&dependencies)

				if out, err := previewUpWith(t, fixture, dependencies, previewUpOptions{name: tc.deploying, persistent: true}); err != nil {
					t.Fatalf("runPreviewUp err = %v; output=%s", err, out)
				}
				if len(*got) == 0 {
					t.Fatal("no app was built, so nothing resolved a value")
				}
				for app, built := range *got {
					if built.Env["PAGE_ID"] != tc.want {
						t.Errorf("%s built with PAGE_ID=%q, want %q", app, built.Env["PAGE_ID"], tc.want)
					}
				}
			})
		}
	})

	t.Run("an override is the only value its own environment needs", func(t *testing.T) {
		fixture, dependencies := setUpPreviewVariablesProject(t, `[{"key":"PAGE_ID","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
		envSet(t, fixture, "PAGE_ID", "page_staging", envOptions{preview: true, environment: "staging"})
		got := captureBuildVariables(&dependencies)

		if out, err := previewUpWith(t, fixture, dependencies, previewUpOptions{name: "staging", persistent: true}); err != nil {
			t.Fatalf("runPreviewUp err = %v, want staging's own override to satisfy the declarations; output=%s", err, out)
		}
		if len(*got) == 0 {
			t.Fatal("no app was built, so nothing resolved a value")
		}
		for app, built := range *got {
			if built.Env["PAGE_ID"] != "page_staging" {
				t.Errorf("%s built with PAGE_ID=%q, want %q", app, built.Env["PAGE_ID"], "page_staging")
			}
		}
	})

	t.Run("a redeployed branch finds the override it already had", func(t *testing.T) {
		fixture, dependencies := setUpPreviewVariablesProject(t, `[{"key":"PAGE_ID","class":"VARIABLE_CLASS_PLAIN","required":true}]`)
		envSet(t, fixture, "PAGE_ID", "page_shared", envOptions{preview: true})
		envSet(t, fixture, "PAGE_ID", "page_staging", envOptions{preview: true, environment: "staging"})
		got := captureBuildVariables(&dependencies)

		up := func(when string) {
			t.Helper()
			*got = nil
			if out, err := previewUpWith(t, fixture, dependencies, previewUpOptions{name: "staging", persistent: true}); err != nil {
				t.Fatalf("runPreviewUp %s err = %v; output=%s", when, err, out)
			}
			if len(*got) == 0 {
				t.Fatalf("no app was built %s, so nothing resolved a value", when)
			}
			for app, built := range *got {
				if built.Env["PAGE_ID"] != "page_staging" {
					t.Errorf("%s built %s with PAGE_ID=%q, want %q", when, app, built.Env["PAGE_ID"], "page_staging")
				}
			}
		}

		up("before the teardown")

		var rm bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &rm)
		if err := runPreviewRemove(context.Background(), dependencies, fixture.Root, previewRemoveOptions{name: "staging", yes: true}, &rm, &rm, strings.NewReader("")); err != nil {
			t.Fatalf("runPreviewRemove err = %v; out=%s", err, rm.String())
		}

		up("after the branch was rebuilt")
	})
}

func nothingToDeployHeadline(t *testing.T, fields string) string {
	t.Helper()
	dependencies := newTestDependencies()
	stubBuild(&dependencies, nil)
	useJSONFormat(t, &dependencies)
	fixture := setUpDeployProject(t)
	writeConfig(t, fixture.Root, fields)
	clitest.WriteFile(t, filepath.Join(clitest.DiscoveryDir(fixture.Root), "main.ts"), "export {};\n")
	writeAppSource(t, fixture.Root, "web", "api")

	out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
	if err != nil {
		t.Fatalf("runDeploy err = %v; output=%s", err, out)
	}
	evs := envelopes(t, out)
	return evs[len(evs)-1].GetSummary().GetHeadline()
}

func TestAProjectWithoutAppsOrResourcesHasNothingToDeploy(t *testing.T) {
	headline := nothingToDeployHeadline(t, "")
	if want := "Nothing to deploy: test-app declares no apps or resources"; headline != want {
		t.Errorf("headline = %q, want %q", headline, want)
	}
}

func TestAppsThatBuildNoFunctionOrImageAreNamedWhenNothingIsLeftToDeploy(t *testing.T) {
	headline := nothingToDeployHeadline(t, `  apps: [
    { name: "web", path: "apps/web", framework: "node" },
    { name: "api", path: "apps/api", framework: "node" },
  ],
`)
	if want := "Nothing to deploy: 2 apps (web and api) built no function or image, and test-app declares no resources"; headline != want {
		t.Errorf("headline = %q, want %q", headline, want)
	}
}

type infisicalProject struct {
	URL string

	mu      sync.Mutex
	secrets map[string]string
	created []string
	refusal string
}

func serveInfisicalProject(t *testing.T, secrets map[string]string) *infisicalProject {
	t.Helper()
	source := &infisicalProject{secrets: map[string]string{}}
	maps.Copy(source.secrets, secrets)
	server := httptest.NewServer(source)
	t.Cleanup(server.Close)
	source.URL = server.URL
	return source
}

func (p *infisicalProject) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mu.Lock()
	defer p.mu.Unlock()
	key := strings.TrimPrefix(r.URL.Path, "/api/v4/secrets/")
	switch {
	case r.URL.Path == "/api/v1/auth/universal-auth/login":
		_ = json.NewEncoder(w).Encode(map[string]any{"accessToken": "token", "expiresIn": 3600})
	case r.Header.Get("Authorization") != "Bearer token":
		w.WriteHeader(http.StatusUnauthorized)
	case r.URL.Path == "/api/v1/projects/p-1":
		_ = json.NewEncoder(w).Encode(map[string]any{"project": map[string]any{"orgId": "org-1"}})
	case p.refusal != "":
		w.WriteHeader(http.StatusServiceUnavailable)
		_ = json.NewEncoder(w).Encode(map[string]any{"message": p.refusal})
	case r.Method == http.MethodGet && r.URL.Path == "/api/v4/secrets":
		secrets := []map[string]any{}
		for key, value := range p.secrets {
			secrets = append(secrets, map[string]any{"id": key, "secretKey": key, "secretValue": value, "version": 1})
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"secrets": secrets})
	case r.Method == http.MethodPost:
		var body map[string]string
		_ = json.NewDecoder(r.Body).Decode(&body)
		p.secrets[key] = body["secretValue"]
		p.created = append(p.created, key+"="+body["secretValue"])
		_ = json.NewEncoder(w).Encode(map[string]any{"secret": map[string]any{"id": key}})
	default:
		w.WriteHeader(http.StatusNotFound)
	}
}

func (p *infisicalProject) refuseReads(message string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.refusal = message
}

func (p *infisicalProject) createdKeys() []string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return slices.Clone(p.created)
}

func (p *infisicalProject) config(write string) string {
	return `
export default {
  slug: "` + clitest.FixtureSlug + `",
  provider: { fake: {} },
  domains: { production: "` + productionDomain + `" },
  envSource: {
    production: { infisical: { project: "p-1", environment: "prod", host: "` + p.URL + `", write: "` + write + `", auth: { universal: { clientId: { $env: "INFISICAL_CLIENT_ID" }, clientSecret: { $env: "INFISICAL_CLIENT_SECRET" } } } } },
  },
};
`
}

const stripeDeclared = `[{"key":"STRIPE_API_KEY","class":"VARIABLE_CLASS_SENSITIVE","required":true,"description":"Stripe's secret key"}]`

func setUpEnvSourceProject(t *testing.T, secrets map[string]string, write string) (clitest.FakeProject, *infisicalProject) {
	t.Helper()
	fixture := setUpVariablesProjectWith(t, stripeDeclared, clitest.EnvDeclareOnlyScript)
	source := serveInfisicalProject(t, secrets)
	clitest.WriteFile(t, filepath.Join(fixture.Root, "ocel.config.ts"), source.config(write))
	envSet(t, fixture, "INFISICAL_CLIENT_ID", "client-id", envOptions{})
	envSet(t, fixture, "INFISICAL_CLIENT_SECRET", "client-secret", envOptions{})
	return fixture, source
}

func registeredEnvSource(t *testing.T, fixture clitest.FakeProject) (envsource.Registration, bool) {
	t.Helper()
	registration, registered, err := envsource.Registered(context.Background(), fixture.Provider.KeyValues(), environment.TierProduction, clitest.FixtureSlug)
	if err != nil {
		t.Fatalf("read the registered env source: %v", err)
	}
	return registration, registered
}

func TestADeployReadsItsTiersEnvSourceBeforeCheckingItsVariables(t *testing.T) {
	t.Run("a value the env source has passes the variables check", func(t *testing.T) {
		fixture, _ := setUpEnvSourceProject(t, map[string]string{"STRIPE_API_KEY": "sk_live_from_the_source"}, "never")

		out, err := deployWith(t, newTestDependencies(), fixture, deployOptions{yes: true})
		if err != nil {
			t.Fatalf("runDeploy err = %v; output=%s", err, out)
		}
		if !strings.Contains(out, "Deployed") {
			t.Errorf("output = %q, want the deploy to complete on the env source's value", out)
		}
		if value := storedValue(t, fixture, "STRIPE_API_KEY"); value.Plaintext != "sk_live_from_the_source" {
			t.Errorf("STRIPE_API_KEY = %q, want the value copied from the env source", value.Plaintext)
		}
	})

	t.Run("a value the env source lacks refuses, naming where to set it", func(t *testing.T) {
		fixture, source := setUpEnvSourceProject(t, nil, "never")

		out, err := deployWith(t, newTestDependencies(), fixture, deployOptions{yes: true})
		if err == nil {
			t.Fatal("runDeploy err = nil, want the variables check to refuse")
		}
		for _, want := range []string{"STRIPE_API_KEY", "set it in infisical:p-1/prod", source.URL} {
			if !strings.Contains(out, want) {
				t.Errorf("output = %q, want %q", out, want)
			}
		}
		if strings.Contains(out, "ocel env set STRIPE_API_KEY") {
			t.Errorf("output = %q, want no `ocel env set` offered for a value the env source owns", out)
		}
	})

	t.Run("what the env source has and nothing declares is a warning, not a refusal", func(t *testing.T) {
		fixture, _ := setUpEnvSourceProject(t, map[string]string{"STRIPE_API_KEY": "sk", "OLD_TOKEN": "old"}, "never")

		out, err := deployWith(t, newTestDependencies(), fixture, deployOptions{yes: true})
		if err != nil {
			t.Fatalf("runDeploy err = %v; output=%s", err, out)
		}
		if !strings.Contains(out, "OLD_TOKEN") || !strings.Contains(out, "nothing this project declares") {
			t.Errorf("output = %q, want OLD_TOKEN reported as undeclared", out)
		}
	})

	t.Run("an env source that cannot be read stops the deploy before anything is built", func(t *testing.T) {
		fixture, source := setUpEnvSourceProject(t, nil, "never")
		source.refuseReads("the env source is down for maintenance")
		dependencies := newTestDependencies()
		built := false
		stubAppBuildRecorder(&dependencies, &built)

		out, err := deployWith(t, dependencies, fixture, deployOptions{yes: true})
		if err == nil || built {
			t.Fatalf("runDeploy err = %v, built = %v, want an unreadable env source to stop the deploy first", err, built)
		}
		if said := out + err.Error(); !strings.Contains(said, "503") && !strings.Contains(said, "down for maintenance") {
			t.Errorf("output = %q, want the env source's failure named", said)
		}
	})

	t.Run("an unset credential stops the deploy with the command that sets it", func(t *testing.T) {
		fixture := setUpVariablesProjectWith(t, stripeDeclared, clitest.EnvDeclareOnlyScript)
		source := serveInfisicalProject(t, nil)
		clitest.WriteFile(t, filepath.Join(fixture.Root, "ocel.config.ts"), source.config("never"))
		envSet(t, fixture, "INFISICAL_CLIENT_ID", "client-id", envOptions{})

		out, err := deployWith(t, newTestDependencies(), fixture, deployOptions{yes: true})
		if err == nil || !strings.Contains(out, "1 variable is not ready") || !strings.Contains(out, "INFISICAL_CLIENT_SECRET  root  no value") {
			t.Fatalf("runDeploy err = %v; output=%s, want the variables check to refuse the unset credential alone", err, out)
		}
		if !strings.Contains(out, "ocel env set INFISICAL_CLIENT_SECRET=<VALUE>") {
			t.Errorf("output = %q, want the credential's command named, not the env source", out)
		}
	})

	t.Run("an unset credential opens the recovery page, and saving it there resumes the deploy", func(t *testing.T) {
		fixture := setUpVariablesProjectWith(t, stripeDeclared, clitest.EnvDeclareOnlyScript)
		source := serveInfisicalProject(t, map[string]string{"STRIPE_API_KEY": "sk_live_from_the_source"})
		clitest.WriteFile(t, filepath.Join(fixture.Root, "ocel.config.ts"), source.config("never"))
		dependencies := newTestDependencies()
		terminalStdin(&dependencies)
		var mu sync.Mutex
		var opened []string
		recordBrowser(&dependencies, &opened, &mu)

		var out syncBuffer
		var stderr bytes.Buffer
		clitest.AttachTerminalSink(dependencies.Invocation, &out)
		done := make(chan error, 1)
		go func() {
			done <- runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &out, &stderr, strings.NewReader(""))
		}()

		address, token := awaitEditorURL(t, &out, 1)
		setCell(t, address, token, "INFISICAL_CLIENT_ID", "client-id")
		setCell(t, address, token, "INFISICAL_CLIENT_SECRET", "client-secret")
		markDone(t, address, token)

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("runDeploy err = %v, want the deploy to resume on the env source's values; stdout=%s", err, out.String())
			}
		case <-time.After(60 * time.Second):
			t.Fatal("runDeploy never returned after the credentials were saved")
		}
		if !strings.Contains(out.String(), "Deployed") {
			t.Errorf("stdout = %q, want the resumed deploy to have completed", out.String())
		}
	})

	t.Run("a writable env source is handed the keys it lacks, empty, for a human to fill", func(t *testing.T) {
		fixture, source := setUpEnvSourceProject(t, nil, "missing")

		out, err := deployWith(t, newTestDependencies(), fixture, deployOptions{yes: true})
		if err == nil {
			t.Fatal("runDeploy err = nil, want the variables check to refuse: an empty key is still unset")
		}
		if created := source.createdKeys(); !slices.Equal(created, []string{"STRIPE_API_KEY="}) {
			t.Fatalf("created = %v, want STRIPE_API_KEY created empty", created)
		}
		if !strings.Contains(out, "Created STRIPE_API_KEY empty in infisical:p-1/prod") {
			t.Errorf("output = %q, want the creation reported", out)
		}
	})

	t.Run("a dry run creates nothing in the env source", func(t *testing.T) {
		fixture, source := setUpEnvSourceProject(t, nil, "missing")

		out, _ := deployWith(t, newTestDependencies(), fixture, deployOptions{yes: true, dry: true})
		if _, registered := registeredEnvSource(t, fixture); !registered || !strings.Contains(out, "STRIPE_API_KEY") {
			t.Fatalf("registered = %v, output = %q, want the dry run to have read the env source and refused", registered, out)
		}
		if created := source.createdKeys(); len(created) != 0 {
			t.Errorf("created = %v, want a dry run to write nothing into the env source", created)
		}
	})

	t.Run("exec runs where ocel deploys, and the provider is handed what it printed", func(t *testing.T) {
		fixture := setUpVariablesProjectWith(t, stripeDeclared, clitest.EnvDeclareOnlyScript)
		clitest.WriteFile(t, filepath.Join(fixture.Root, "ocel.config.ts"), `
export default {
  slug: "`+clitest.FixtureSlug+`",
  provider: { fake: {} },
  domains: { production: "`+productionDomain+`" },
  envSource: {
    production: { exec: { command: ["sh", "-c", "printf 'STRIPE_API_KEY=sk_from_exec'"], format: "dotenv" } },
  },
};
`)

		if out, err := deployWith(t, newTestDependencies(), fixture, deployOptions{yes: true}); err != nil {
			t.Fatalf("runDeploy err = %v; output=%s", err, out)
		}
		value := storedValue(t, fixture, "STRIPE_API_KEY")
		if value.Plaintext != "sk_from_exec" || value.Provenance.EnvSource != "exec" {
			t.Errorf("STRIPE_API_KEY = %q from %q, want the value copied from exec", value.Plaintext, value.Provenance.EnvSource)
		}
	})

	t.Run("a tier back on builtin forgets the env source it read before", func(t *testing.T) {
		fixture, _ := setUpEnvSourceProject(t, map[string]string{"STRIPE_API_KEY": "sk"}, "never")
		if out, err := deployWith(t, newTestDependencies(), fixture, deployOptions{yes: true}); err != nil {
			t.Fatalf("runDeploy err = %v; output=%s", err, out)
		}
		writeConfig(t, fixture.Root, "")

		if out, err := deployWith(t, newTestDependencies(), fixture, deployOptions{yes: true}); err != nil {
			t.Fatalf("runDeploy err = %v; output=%s", err, out)
		}
		if registration, registered := registeredEnvSource(t, fixture); registered {
			t.Errorf("production still reads from %s, want its registration forgotten", registration.Descriptor.ID())
		}
	})
}

func stubAppBuildRecorder(dependencies *Dependencies, built *bool) {
	stubRecordedDeploymentIDs(dependencies)
	dependencies.BuildApps = func(_ context.Context, cfg *project.Project, _ map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
		*built = true
		return functionsOnDisk(cfg)
	}
}

func captureBuildVariables(dependencies *Dependencies) *map[string]build.AppVariables {
	stubRecordedDeploymentIDs(dependencies)
	var got map[string]build.AppVariables
	dependencies.BuildApps = func(_ context.Context, cfg *project.Project, variables map[string]build.AppVariables, _ map[string]string, _ build.HostedWorkers, _ build.Host, _ build.Log) (build.Output, error) {
		got = variables
		return functionsOnDisk(cfg)
	}
	return &got
}

func ownedElsewhere(t *testing.T, fixture clitest.FakeProject, owner, key, value string) {
	t.Helper()
	setValue(t, fixture, variablestore.Scope{Project: owner, Tier: environment.TierProduction}, key, value, envOptions{})
}

func envRef(t *testing.T, fixture clitest.FakeProject, key, owner string) {
	t.Helper()
	scope := variablestore.Scope{Project: clitest.FixtureSlug, Tier: environment.TierProduction}
	at := variablestore.Coordinate{Cell: variablestore.Cell{Key: key}}
	if _, err := valueStore(fixture).SetReference(context.Background(), scope, at, variablestore.Target{Project: owner, Cell: variablestore.Cell{Key: key}}); err != nil {
		t.Fatalf("reference %s in %s: %v", key, owner, err)
	}
}

func storedValue(t *testing.T, fixture clitest.FakeProject, key string) variablestore.Value {
	t.Helper()
	scope := variablestore.Scope{Project: clitest.FixtureSlug, Tier: environment.TierProduction}
	value, err := valueStore(fixture).Get(context.Background(), scope, variablestore.Coordinate{Cell: variablestore.Cell{Key: key}}, true)
	if err != nil {
		t.Fatalf("read %s: %v", key, err)
	}
	return value
}

func recordingHost(dependencies *Dependencies) *build.Host {
	handed := &build.Host{}
	buildApps := dependencies.BuildApps
	dependencies.BuildApps = func(ctx context.Context, cfg *project.Project, variables map[string]build.AppVariables, archs map[string]string, workers build.HostedWorkers, host build.Host, log build.Log) (build.Output, error) {
		*handed = host
		return buildApps(ctx, cfg, variables, archs, workers, host, log)
	}
	return handed
}

func TestDeployBuildsForTheFunctionHostItsProviderDeclares(t *testing.T) {
	fixture := setUpDeployProject(t)
	fixture.Provider.WithFacts(func(facts *provider.Facts) {
		facts.NextRuntimeDir = "/var/host/next"
		facts.MaxFunctionBytes = 200 << 20
		facts.NextRefreshesByRequest = true
	})
	addAppToFixtureConfig(t, fixture.Root)
	dependencies := newTestDependencies()
	stubBuild(&dependencies, apiFunction())
	handed := recordingHost(&dependencies)

	var stdout, stderr bytes.Buffer
	clitest.AttachTerminalSink(dependencies.Invocation, &stdout)
	if err := runDeploy(context.Background(), dependencies, fixture.Root, deployOptions{yes: true}, &stdout, &stderr, strings.NewReader("")); err != nil {
		t.Fatalf("runDeploy err = %v; stdout=%s stderr=%s", err, stdout.String(), stderr.String())
	}
	if handed.NextRuntimeDir != "/var/host/next" {
		t.Errorf("the build was handed the Next runtime directory %q, want the one the provider declares", handed.NextRuntimeDir)
	}
	if handed.MaxFunctionBytes != 200<<20 {
		t.Errorf("the build was handed a size budget of %d bytes, want the one the provider declares", handed.MaxFunctionBytes)
	}
	if !handed.NextRefreshesByRequest {
		t.Error("the build was not told its host refreshes by request, which the provider declares")
	}
}

func TestPreviewUpBuildsForTheFunctionHostItsProviderDeclares(t *testing.T) {
	fixture := setUpPreviewProject(t)
	fixture.Provider.WithFacts(func(facts *provider.Facts) {
		facts.NextRuntimeDir = "/var/host/next"
		facts.MaxFunctionBytes = 200 << 20
		facts.NextRefreshesByRequest = true
	})
	addAppToFixtureConfig(t, fixture.Root)
	dependencies := newTestDependencies()
	stubBuild(&dependencies, apiFunction())
	stubGit(&dependencies, "feature", "")
	handed := recordingHost(&dependencies)

	previewUp(t, fixture, dependencies, previewUpOptions{name: "staging", persistent: true})
	if handed.NextRuntimeDir != "/var/host/next" {
		t.Errorf("the build was handed the Next runtime directory %q, want the one the provider declares", handed.NextRuntimeDir)
	}
	if handed.MaxFunctionBytes != 200<<20 {
		t.Errorf("the build was handed a size budget of %d bytes, want the one the provider declares", handed.MaxFunctionBytes)
	}
	if !handed.NextRefreshesByRequest {
		t.Error("the build was not told its host refreshes by request, which the provider declares")
	}
}
