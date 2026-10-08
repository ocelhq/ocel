package clitest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"

	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

const FixtureConsoleProjectID = "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a79"

type FakeConsole struct {
	URL string

	mu      sync.Mutex
	reports []*consolev1.ReportRequest
	events  []*consolev1.RecordEnvironmentEventRequest
}

func ServeConsole(t *testing.T) *FakeConsole {
	t.Helper()
	fake := &FakeConsole{}
	path, handler := consolev1connect.NewDeploymentServiceHandler(fake, connect.WithInterceptors(connectvalidate.NewInterceptor()))
	mux := http.NewServeMux()
	mux.Handle(path, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	fake.URL = srv.URL
	return fake
}

func (f *FakeConsole) Report(_ context.Context, req *consolev1.ReportRequest) (*consolev1.ReportResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.reports = append(f.reports, req)
	return &consolev1.ReportResponse{}, nil
}

func (f *FakeConsole) RecordEnvironmentEvent(_ context.Context, req *consolev1.RecordEnvironmentEventRequest) (*consolev1.RecordEnvironmentEventResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.events = append(f.events, req)
	return &consolev1.RecordEnvironmentEventResponse{}, nil
}

func (f *FakeConsole) Reports() []*consolev1.ReportRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*consolev1.ReportRequest(nil), f.reports...)
}

func (f *FakeConsole) Events() []*consolev1.RecordEnvironmentEventRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*consolev1.RecordEnvironmentEventRequest(nil), f.events...)
}

func (f *FakeConsole) Link(t *testing.T, projectDir string) {
	t.Helper()
	LinkToConsole(t, projectDir, f.URL)
}

func LinkToConsole(t *testing.T, projectDir, apiURL string) {
	t.Helper()
	link := console.Link{APIURL: apiURL, OrganizationID: "org_1", ProjectID: FixtureConsoleProjectID, ProjectName: "fixture"}
	if err := console.WriteLink(projectDir, link); err != nil {
		t.Fatal(err)
	}
}

func SignedInTo(apiURL string) deployreport.Console {
	return deployreport.Console{LoadCredentials: func() (console.Credentials, error) {
		return console.Credentials{AccessToken: "device-session", APIURL: apiURL}, nil
	}}
}
