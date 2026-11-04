package runner_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/endpoint/endpointtest"
	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	"github.com/ocelhq/ocel/pkg/runner"
	"github.com/ocelhq/ocel/pkg/runner/conformance"
	"github.com/ocelhq/ocel/pkg/scope"
)

type tokens struct {
	console *endpointtest.Console

	mu      sync.Mutex
	granted map[string]bool
}

func newTokens(t *testing.T) *tokens {
	return &tokens{console: endpointtest.NewConsole(t), granted: map[string]bool{}}
}

func (k *tokens) record(token string) string {
	k.mu.Lock()
	defer k.mu.Unlock()
	k.granted[token] = true
	return token
}

func (k *tokens) person(t *testing.T, organization string) string {
	return k.personAs(t, organization, scopev1.Role_ROLE_OWNER)
}

func (k *tokens) personAs(t *testing.T, organization string, role scopev1.Role) string {
	var scopes []scopev1.Scope
	for _, held := range []scopev1.Scope{scopev1.Scope_SCOPE_APPS_READ, scopev1.Scope_SCOPE_APPS_WRITE, scopev1.Scope_SCOPE_VARIABLES_WRITE} {
		if role >= scope.LeastRole(held) {
			scopes = append(scopes, held)
		}
	}
	return k.record(k.console.PersonToken(t, "runner-test", organization, "user-1", role, scopes))
}

func (k *tokens) job(t *testing.T, organization string, job endpoint.Job) string {
	return k.record(k.console.JobToken(t, organization, job))
}

type idleLauncher struct {
	mu             sync.Mutex
	failing        int
	failingCancels int
	launched       []runner.Launch
	canceled       []string
}

func (l *idleLauncher) Launch(_ context.Context, launch runner.Launch) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.failing > 0 {
		l.failing--
		return "", errors.New("the docker daemon did not answer")
	}
	l.launched = append(l.launched, launch)
	return "agent-" + launch.JobID, nil
}

func (l *idleLauncher) Cancel(_ context.Context, handle string) error {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.canceled = append(l.canceled, handle)
	if l.failingCancels > 0 {
		l.failingCancels--
		return errors.New("the docker daemon did not answer")
	}
	return nil
}

func (l *idleLauncher) cancels() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.canceled)
}

type testRunner struct {
	server   *runner.Server
	url      string
	tokens   *tokens
	launcher *idleLauncher
	now      *clock
}

type clock struct {
	mu sync.Mutex
	at time.Time
}

func (c *clock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.at.IsZero() {
		return time.Now()
	}
	return c.at
}

func (c *clock) set(at time.Time) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = at
}

func newTestRunner(t *testing.T, dir string) *testRunner {
	t.Helper()
	return newTestRunnerTrusting(t, dir, newTokens(t))
}

func newTestRunnerTrusting(t *testing.T, dir string, trusted *tokens, configure ...func(*runner.Config)) *testRunner {
	t.Helper()
	r := &testRunner{tokens: trusted, launcher: &idleLauncher{}, now: &clock{}}
	var handler http.Handler
	httpServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, req *http.Request) { handler.ServeHTTP(w, req) }))
	t.Cleanup(httpServer.Close)
	r.url = httpServer.URL
	config := runner.Config{
		ID: "runner-test", Dir: dir, URL: httpServer.URL, Launcher: r.launcher, Now: r.now.Now, MaxJobs: 64, CancelGrace: 50 * time.Millisecond,
		StoreVariables: func(context.Context, runner.VariableValues) error { return nil },
	}
	for _, each := range configure {
		each(&config)
	}
	server, err := runner.New(config)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(server.Close)
	runnerPath, runnerHandler := server.RunnerServiceHandler()
	agentPath, agentHandler := server.JobAgentServiceHandler()
	blobsPath, blobsHandler := server.BlobsHandler()
	handler, err = endpoint.New(endpoint.Spec{
		Config:                  endpoint.Config{Console: r.tokens.console.URL, EndpointID: "runner-test", AllowedScopes: scope.ClaimsOf(scope.ListUserScopes())},
		ServesManyOrganizations: true,
		Mounts: []endpoint.Mount{
			{Path: runnerPath, Handler: runnerHandler},
			{Path: agentPath, Handler: agentHandler},
			{Path: blobsPath, Handler: blobsHandler, SelfAuthenticated: true},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	r.server = server
	return r
}

func TestTheRunnerConformsToTheRunnerContract(t *testing.T) {
	r := newTestRunner(t, t.TempDir())
	conformance.Run(t, conformance.Suite{
		URL: r.url, Runner: "runner-test", Organization: "org-a", OtherOrganization: "org-b",
		PersonToken: r.tokens.personAs, JobToken: r.tokens.job,
	})
}
