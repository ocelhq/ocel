package clitest

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/deployreport"
	"github.com/ocelhq/ocel/cli/internal/pricecache/pricecachetest"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

const FixtureConsoleProjectID = "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a79"

const FixtureConsoleTargetID = "0199c3a2-5b7e-7c4d-8a1f-2e3d4c5b6a80"

type FakeConsole struct {
	consolev1connect.UnimplementedTargetServiceHandler

	URL    string
	Prices *pricecachetest.Console

	Credential        *consolev1.IssueCredentialResponse
	CredentialRefusal *connect.Error

	mu        sync.Mutex
	reports   []*consolev1.ReportRequest
	events    []*consolev1.RecordEnvironmentEventRequest
	issued    []*consolev1.IssueCredentialRequest
	prepared  []*consolev1.PrepareTargetRequest
	recorded  []*consolev1.RecordTargetTierRequest
	forgotten []*consolev1.ForgetTargetTierRequest
}

func ServeConsole(t *testing.T) *FakeConsole {
	t.Helper()
	fake := &FakeConsole{Prices: pricecachetest.New(nil)}
	validated := connect.WithInterceptors(connectvalidate.NewInterceptor())
	path, handler := consolev1connect.NewDeploymentServiceHandler(fake, validated)
	targetsPath, targets := consolev1connect.NewTargetServiceHandler(fake)
	mux := http.NewServeMux()
	mux.Handle("/api/connect"+path, http.StripPrefix("/api/connect", handler))
	mux.Handle("/api/connect"+targetsPath, http.StripPrefix("/api/connect", targets))
	fake.Prices.Mount(mux)
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

func (f *FakeConsole) IssueCredential(_ context.Context, req *consolev1.IssueCredentialRequest) (*consolev1.IssueCredentialResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.issued = append(f.issued, req)
	if f.CredentialRefusal != nil {
		return nil, f.CredentialRefusal
	}
	return f.Credential, nil
}

func (f *FakeConsole) Prepare(_ context.Context, req *consolev1.PrepareTargetRequest) (*consolev1.PrepareTargetResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.prepared = append(f.prepared, req)
	return &consolev1.PrepareTargetResponse{
		TargetId:       FixtureConsoleTargetID,
		OrganizationId: "org_1",
		Issuer:         f.URL + "/oidc",
		Audience:       f.URL,
	}, nil
}

func (f *FakeConsole) RecordTier(_ context.Context, req *consolev1.RecordTargetTierRequest) (*consolev1.RecordTargetTierResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.recorded = append(f.recorded, req)
	return &consolev1.RecordTargetTierResponse{}, nil
}

func (f *FakeConsole) ForgetTier(_ context.Context, req *consolev1.ForgetTargetTierRequest) (*consolev1.ForgetTargetTierResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.forgotten = append(f.forgotten, req)
	return &consolev1.ForgetTargetTierResponse{}, nil
}

func (f *FakeConsole) ForgottenTiers() []*consolev1.ForgetTargetTierRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*consolev1.ForgetTargetTierRequest(nil), f.forgotten...)
}

func (f *FakeConsole) PreparedTargets() []*consolev1.PrepareTargetRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*consolev1.PrepareTargetRequest(nil), f.prepared...)
}

func (f *FakeConsole) RecordedTiers() []*consolev1.RecordTargetTierRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*consolev1.RecordTargetTierRequest(nil), f.recorded...)
}

func (f *FakeConsole) IssuedCredentials() []*consolev1.IssueCredentialRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*consolev1.IssueCredentialRequest(nil), f.issued...)
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

func ReadDeployReport(t *testing.T, path string) *consolev1.Deployment {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read the deploy report: %v", err)
	}
	var report consolev1.Deployment
	if err := protojson.Unmarshal(raw, &report); err != nil {
		t.Fatalf("the deploy report is not a deployment's protojson: %v", err)
	}
	return &report
}
