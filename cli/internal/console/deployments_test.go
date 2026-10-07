package console

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/timestamppb"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

const (
	deploymentProjectID = "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a79"
	deploymentTraceID   = "4bf92f3577b34da6a3ce929d0e0e4736"
)

type recordedReports struct {
	mu      sync.Mutex
	reports []*consolev1.ReportRequest
	events  []*consolev1.RecordEnvironmentEventRequest
	auth    []string
	agents  []string
	answer  error
}

func (s *recordedReports) Report(_ context.Context, req *consolev1.ReportRequest) (*consolev1.ReportResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.reports = append(s.reports, req)
	if s.answer != nil {
		return nil, s.answer
	}
	return &consolev1.ReportResponse{}, nil
}

func (s *recordedReports) RecordEnvironmentEvent(_ context.Context, req *consolev1.RecordEnvironmentEventRequest) (*consolev1.RecordEnvironmentEventResponse, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.events = append(s.events, req)
	if s.answer != nil {
		return nil, s.answer
	}
	return &consolev1.RecordEnvironmentEventResponse{}, nil
}

func serveDeployments(t *testing.T, service *recordedReports) string {
	t.Helper()
	path, handler := consolev1connect.NewDeploymentServiceHandler(service)
	mux := http.NewServeMux()
	mux.Handle(path, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		service.mu.Lock()
		service.auth = append(service.auth, r.Header.Get("Authorization"))
		service.agents = append(service.agents, r.Header.Get("User-Agent"))
		service.mu.Unlock()
		handler.ServeHTTP(w, r)
	}))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv.URL
}

func TestReportDeploymentSendsTheRecordWithTheSessionBearerAndTheProjectID(t *testing.T) {
	t.Parallel()
	service := &recordedReports{}
	client := New(serveDeployments(t, service))

	deployment := &consolev1.Deployment{Id: deploymentTraceID, Kind: consolev1.DeploymentKind_DEPLOYMENT_KIND_DEPLOY}
	if err := client.ReportDeployment(context.Background(), "tok", deploymentProjectID, deployment); err != nil {
		t.Fatalf("ReportDeployment() error = %v", err)
	}

	if len(service.reports) != 1 {
		t.Fatalf("the console received %d reports, want 1", len(service.reports))
	}
	got := service.reports[0]
	if got.GetProjectId() != deploymentProjectID || got.GetDeployment().GetId() != deploymentTraceID {
		t.Errorf("the console received project %q deployment %q, want %q and %q", got.GetProjectId(), got.GetDeployment().GetId(), deploymentProjectID, deploymentTraceID)
	}
	if service.auth[0] != "Bearer tok" {
		t.Errorf("Authorization = %q, want %q", service.auth[0], "Bearer tok")
	}
	if !strings.HasPrefix(service.agents[0], "ocel-cli") {
		t.Errorf("User-Agent = %q, want the CLI's", service.agents[0])
	}
}

func TestReportDeploymentReturnsTheConsolesRefusal(t *testing.T) {
	t.Parallel()
	service := &recordedReports{answer: connect.NewError(connect.CodeUnauthenticated, errors.New("session expired"))}
	client := New(serveDeployments(t, service))

	err := client.ReportDeployment(context.Background(), "tok", deploymentProjectID, &consolev1.Deployment{})
	if connect.CodeOf(err) != connect.CodeUnauthenticated {
		t.Fatalf("ReportDeployment() = %v, want the console's unauthenticated answer", err)
	}
}

func TestReportDeploymentFailsWhenTheConsoleIsUnreachable(t *testing.T) {
	t.Parallel()
	srv := httptest.NewServer(http.NotFoundHandler())
	url := srv.URL
	srv.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if err := New(url).ReportDeployment(ctx, "tok", deploymentProjectID, &consolev1.Deployment{}); err == nil {
		t.Fatal("ReportDeployment() = nil, want an error from a console nothing answers at")
	}
}

func TestRecordEnvironmentEventSendsTheEventWithTheSessionBearer(t *testing.T) {
	t.Parallel()
	service := &recordedReports{}
	client := New(serveDeployments(t, service))

	event := &consolev1.EnvironmentEvent{
		Id:          deploymentTraceID,
		Kind:        consolev1.EnvironmentEventKind_ENVIRONMENT_EVENT_KIND_DESTROYED,
		Environment: &environmentv1.Environment{Tier: environmentv1.Tier_TIER_PRODUCTION},
		At:          timestamppb.Now(),
	}
	if err := client.RecordEnvironmentEvent(context.Background(), "tok", deploymentProjectID, event); err != nil {
		t.Fatalf("RecordEnvironmentEvent() error = %v", err)
	}

	if len(service.events) != 1 || service.events[0].GetEvent().GetKind() != consolev1.EnvironmentEventKind_ENVIRONMENT_EVENT_KIND_DESTROYED {
		t.Fatalf("the console received events %v, want one destroyed event", service.events)
	}
	if service.events[0].GetProjectId() != deploymentProjectID || service.auth[0] != "Bearer tok" {
		t.Errorf("the event arrived for project %q with Authorization %q", service.events[0].GetProjectId(), service.auth[0])
	}
}
