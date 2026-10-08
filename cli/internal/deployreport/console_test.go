package deployreport

import (
	"bytes"
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/cli/internal/console"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

const consoleProjectID = "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a79"

type fakeConsole struct {
	mu      sync.Mutex
	reports []*consolev1.ReportRequest
	events  []*consolev1.RecordEnvironmentEventRequest
	auth    []string
	answer  error
	hang    chan struct{}
}

func (f *fakeConsole) Report(ctx context.Context, req *consolev1.ReportRequest) (*consolev1.ReportResponse, error) {
	f.mu.Lock()
	f.reports = append(f.reports, req)
	f.mu.Unlock()
	if f.hang != nil {
		select {
		case <-f.hang:
		case <-ctx.Done():
		}
	}
	if f.answer != nil {
		return nil, f.answer
	}
	return &consolev1.ReportResponse{}, nil
}

func (f *fakeConsole) RecordEnvironmentEvent(_ context.Context, req *consolev1.RecordEnvironmentEventRequest) (*consolev1.RecordEnvironmentEventResponse, error) {
	f.mu.Lock()
	f.events = append(f.events, req)
	f.mu.Unlock()
	if f.answer != nil {
		return nil, f.answer
	}
	return &consolev1.RecordEnvironmentEventResponse{}, nil
}

func serveConsole(t *testing.T, fake *fakeConsole) string {
	t.Helper()
	path, handler := consolev1connect.NewDeploymentServiceHandler(fake, connect.WithInterceptors(connectvalidate.NewInterceptor()))
	mux := http.NewServeMux()
	mux.Handle("/api/connect"+path, http.StripPrefix("/api/connect", http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.auth = append(fake.auth, r.Header.Get("Authorization"))
		fake.mu.Unlock()
		handler.ServeHTTP(w, r)
	})))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func linkedTree(t *testing.T, apiURL string) string {
	t.Helper()
	dir := t.TempDir()
	link := console.Link{APIURL: apiURL, OrganizationID: "org_1", ProjectID: consoleProjectID, ProjectName: "shop"}
	if err := console.WriteLink(dir, link); err != nil {
		t.Fatal(err)
	}
	return dir
}

func signedIn(apiURL string) Console {
	return Console{LoadCredentials: func() (console.Credentials, error) {
		return console.Credentials{AccessToken: "tok", APIURL: apiURL}, nil
	}}
}

func TestALinkedTreeReportsItsDeploymentOnceWithTheDeviceSession(t *testing.T) {
	t.Parallel()
	fake := &fakeConsole{}
	apiURL := serveConsole(t, fake)
	dir := linkedTree(t, apiURL)
	var stderr bytes.Buffer

	signedIn(apiURL).ReportDeployment(context.Background(), dir, productionAttempt().Succeeded(finishedAt), &stderr)

	if len(fake.reports) != 1 {
		t.Fatalf("the console received %d reports, want 1 (stderr %q)", len(fake.reports), stderr.String())
	}
	if got := fake.reports[0]; got.GetProjectId() != consoleProjectID || got.GetDeployment().GetId() != traceID {
		t.Errorf("the console received project %q deployment %q, want %q and %q", got.GetProjectId(), got.GetDeployment().GetId(), consoleProjectID, traceID)
	}
	if fake.auth[0] != "Bearer tok" {
		t.Errorf("Authorization = %q, want the device session", fake.auth[0])
	}
	if stderr.Len() != 0 {
		t.Errorf("stderr = %q, want nothing from a report that went through", stderr.String())
	}
}

func TestAnUnlinkedTreeMakesNoCallAndPrintsOneHintNamingOcelLink(t *testing.T) {
	t.Parallel()
	fake := &fakeConsole{}
	apiURL := serveConsole(t, fake)
	var stderr bytes.Buffer

	signedIn(apiURL).ReportDeployment(context.Background(), t.TempDir(), productionAttempt().Succeeded(finishedAt), &stderr)

	if len(fake.reports) != 0 {
		t.Fatalf("the console received %d reports from an unlinked tree, want none", len(fake.reports))
	}
	if lines := strings.Split(strings.TrimSpace(stderr.String()), "\n"); len(lines) != 1 || !strings.Contains(lines[0], "`ocel link`") {
		t.Errorf("stderr = %q, want one hint line naming `ocel link`", stderr.String())
	}
}

func TestATreeLinkedToAnotherConsoleCountsAsUnlinked(t *testing.T) {
	t.Parallel()
	fake := &fakeConsole{}
	apiURL := serveConsole(t, fake)
	dir := linkedTree(t, "https://other.example.com")
	var stderr bytes.Buffer

	signedIn(apiURL).ReportDeployment(context.Background(), dir, productionAttempt().Succeeded(finishedAt), &stderr)

	if len(fake.reports) != 0 || !strings.Contains(stderr.String(), "`ocel link`") {
		t.Errorf("reports %d, stderr %q, want no call and the link hint", len(fake.reports), stderr.String())
	}
}

func TestAnUnreachableConsoleLeavesOneWarningNamingTheDeployment(t *testing.T) {
	t.Parallel()
	down := httptest.NewServer(http.NotFoundHandler())
	apiURL := down.URL
	down.Close()
	dir := linkedTree(t, apiURL)
	var stderr bytes.Buffer

	signedIn(apiURL).ReportDeployment(context.Background(), dir, productionAttempt().Succeeded(finishedAt), &stderr)

	lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], traceID) {
		t.Errorf("stderr = %q, want one warning line naming deployment %s", stderr.String(), traceID)
	}
}

func TestAConsoleThatRefusesTheRecordLeavesOneWarningNamingTheDeployment(t *testing.T) {
	t.Parallel()
	fake := &fakeConsole{answer: connect.NewError(connect.CodeUnauthenticated, errors.New("session expired"))}
	apiURL := serveConsole(t, fake)
	dir := linkedTree(t, apiURL)
	var stderr bytes.Buffer

	signedIn(apiURL).ReportDeployment(context.Background(), dir, productionAttempt().Succeeded(finishedAt), &stderr)

	lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
	if len(fake.reports) != 1 || len(lines) != 1 || !strings.Contains(lines[0], traceID) || !strings.Contains(lines[0], "session expired") {
		t.Errorf("reports %d, stderr %q, want one attempt and one warning naming %s and the console's reason", len(fake.reports), stderr.String(), traceID)
	}
}

func TestAConsoleThatNeverAnswersIsGivenUpOnAfterTheTimeout(t *testing.T) {
	t.Parallel()
	fake := &fakeConsole{hang: make(chan struct{})}
	t.Cleanup(func() { close(fake.hang) })
	apiURL := serveConsole(t, fake)
	dir := linkedTree(t, apiURL)
	var stderr bytes.Buffer
	reporting := signedIn(apiURL)
	reporting.Timeout = 50 * time.Millisecond

	started := time.Now()
	reporting.ReportDeployment(context.Background(), dir, productionAttempt().Succeeded(finishedAt), &stderr)

	if took := time.Since(started); took > 5*time.Second {
		t.Errorf("reporting took %v, want it cut off at its timeout", took)
	}
	if !strings.Contains(stderr.String(), traceID) {
		t.Errorf("stderr = %q, want a warning naming deployment %s", stderr.String(), traceID)
	}
}

func TestALinkedTreeThatIsNotLoggedInLeavesOneWarningNamingTheDeployment(t *testing.T) {
	t.Parallel()
	fake := &fakeConsole{}
	apiURL := serveConsole(t, fake)
	dir := linkedTree(t, apiURL)
	var stderr bytes.Buffer
	reporting := Console{LoadCredentials: func() (console.Credentials, error) {
		return console.Credentials{APIURL: apiURL}, console.ErrNotLoggedIn
	}}

	reporting.ReportDeployment(context.Background(), dir, productionAttempt().Succeeded(finishedAt), &stderr)

	if len(fake.reports) != 0 || !strings.Contains(stderr.String(), traceID) || !strings.Contains(stderr.String(), "ocel login") {
		t.Errorf("reports %d, stderr %q, want no call and a warning naming %s and `ocel login`", len(fake.reports), stderr.String(), traceID)
	}
}

func TestACancelledRunStillReportsItsFailedDeployment(t *testing.T) {
	t.Parallel()
	fake := &fakeConsole{}
	apiURL := serveConsole(t, fake)
	dir := linkedTree(t, apiURL)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	signedIn(apiURL).ReportDeployment(ctx, dir, productionAttempt().Failed(finishedAt, context.Canceled), &bytes.Buffer{})

	if len(fake.reports) != 1 {
		t.Errorf("the console received %d reports, want the failed deployment of the cancelled run", len(fake.reports))
	}
}

func TestAnEnvironmentEventFollowsTheSameLinkedAndUnlinkedRules(t *testing.T) {
	t.Parallel()
	fake := &fakeConsole{}
	apiURL := serveConsole(t, fake)
	event := &consolev1.EnvironmentEvent{
		Id:          traceID,
		Kind:        consolev1.EnvironmentEventKind_ENVIRONMENT_EVENT_KIND_PREVIEW_REMOVED,
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PREVIEW, Identity: "pr-12"},
		At:          timestamppb.New(finishedAt),
	}
	var linkedErr, unlinkedErr bytes.Buffer

	signedIn(apiURL).ReportEnvironmentEvent(context.Background(), linkedTree(t, apiURL), event, &linkedErr)
	signedIn(apiURL).ReportEnvironmentEvent(context.Background(), t.TempDir(), event, &unlinkedErr)

	if len(fake.events) != 1 || fake.events[0].GetProjectId() != consoleProjectID || linkedErr.Len() != 0 {
		t.Errorf("events %d, linked stderr %q, want one event for the linked project and no output", len(fake.events), linkedErr.String())
	}
	if !strings.Contains(unlinkedErr.String(), "`ocel link`") {
		t.Errorf("unlinked stderr = %q, want the link hint", unlinkedErr.String())
	}
}

func TestADeploymentThatNamesNoTargetMakesNoCallAndLeavesOneWarningNamingIt(t *testing.T) {
	t.Parallel()
	fake := &fakeConsole{}
	apiURL := serveConsole(t, fake)
	dir := linkedTree(t, apiURL)
	attempt := productionAttempt()
	attempt.Target = ""
	var stderr bytes.Buffer

	signedIn(apiURL).ReportDeployment(context.Background(), dir, attempt.Succeeded(finishedAt), &stderr)

	if len(fake.reports) != 0 {
		t.Errorf("the console received %d reports, want none for a record it would refuse", len(fake.reports))
	}
	lines := strings.Split(strings.TrimSpace(stderr.String()), "\n")
	if len(lines) != 1 || !strings.Contains(lines[0], traceID) || !strings.Contains(lines[0], "target") {
		t.Errorf("stderr = %q, want one warning naming deployment %s and its missing target", stderr.String(), traceID)
	}
}

func TestATreeLinkedToAnotherConsoleThatIsNotLoggedInIsToldToLogIn(t *testing.T) {
	t.Parallel()
	fake := &fakeConsole{}
	apiURL := serveConsole(t, fake)
	dir := linkedTree(t, apiURL)
	reporting := Console{LoadCredentials: func() (console.Credentials, error) {
		return console.Credentials{}, console.ErrNotLoggedIn
	}}
	var stderr bytes.Buffer

	reporting.ReportDeployment(context.Background(), dir, productionAttempt().Succeeded(finishedAt), &stderr)

	if len(fake.reports) != 0 || !strings.Contains(stderr.String(), "`ocel login`") || strings.Contains(stderr.String(), "`ocel link`") {
		t.Errorf("reports %d, stderr %q, want no call and the login warning rather than the link hint", len(fake.reports), stderr.String())
	}
}
