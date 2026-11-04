package clitest

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"slices"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/console"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/scope"
)

func (f *FakeConsole) mountAccounts(mux *http.ServeMux) {
	mux.HandleFunc("/api/auth/organization/list", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_ = json.NewEncoder(w).Encode(f.Organizations)
	})
	mux.HandleFunc("/api/auth/get-session", func(w http.ResponseWriter, _ *http.Request) {
		f.mu.Lock()
		defer f.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"session":{"activeOrganizationId":%q},"user":{"email":"dev@acme.test"}}`, f.ActiveOrg)
	})
	mux.HandleFunc("/api/auth/organization/set-active", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OrganizationID string `json:"organizationId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		f.mu.Lock()
		f.ActivatedOrgs = append(f.ActivatedOrgs, body.OrganizationID)
		f.mu.Unlock()
		_, _ = w.Write([]byte("{}"))
	})
}

func (f *FakeConsole) LogIn(invocation *commands.Invocation) {
	invocation.Console.LoadCredentials = func() (console.Credentials, error) {
		return console.Credentials{AccessToken: "device-session", APIURL: f.URL, Email: "dev@acme.test"}, nil
	}
	invocation.CloudOffer = func(context.Context, console.Credentials) (*console.CloudOffer, error) {
		f.mu.Lock()
		defer f.mu.Unlock()
		return &console.CloudOffer{APIURL: f.Runner.URL, ProviderURL: f.ProviderURL, Audience: "ocel-cloud", Version: f.RunnerVersion}, nil
	}
}

func (f *FakeConsole) BindTestRunner(tier environmentv1.Tier, credentials bool) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.Bindings == nil {
		f.Bindings = map[environmentv1.Tier]*consolev1.ResolveRunnerResponse{}
	}
	f.Bindings[tier] = &consolev1.ResolveRunnerResponse{Credentials: credentials, Runner: &consolev1.Endpoint{
		Id: FakeRunnerID, Name: "build-box", Url: proto.String(f.Runner.URL), Version: proto.String(f.RunnerVersion),
	}}
	return FakeRunnerID
}

type fakeConsoleService struct {
	consolev1connect.UnimplementedConsoleServiceHandler
	console *FakeConsole
}

func (s fakeConsoleService) Describe(context.Context, *consolev1.DescribeRequest) (*consolev1.DescribeResponse, error) {
	f := s.console
	f.mu.Lock()
	defer f.mu.Unlock()
	return &consolev1.DescribeResponse{Cloud: &consolev1.CloudOffer{ApiUrl: f.Runner.URL, ProviderUrl: f.ProviderURL, Audience: "ocel-cloud", Version: f.RunnerVersion}}, nil
}

func (s fakeConsoleService) IssueEndpointToken(_ context.Context, req *consolev1.IssueEndpointTokenRequest) (*consolev1.IssueEndpointTokenResponse, error) {
	f := s.console
	f.mu.Lock()
	defer f.mu.Unlock()
	claims := scope.ClaimsOf(req.GetScopes())
	for _, claim := range claims {
		if slices.Contains(f.RefusedClaims, claim) {
			return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("your role holds no %s", claim))
		}
	}
	f.TokenOrgs = append(f.TokenOrgs, req.GetOrganizationId())
	f.TokenEndpoints = append(f.TokenEndpoints, req.GetEndpointId())
	f.TokenScopes = append(f.TokenScopes, claims)
	return &consolev1.IssueEndpointTokenResponse{Token: scopedToken(FakeCloudToken, req.GetScopes()), ExpiresAt: timestamppb.New(time.Now().Add(time.Hour))}, nil
}

type fakeProjects struct {
	consolev1connect.UnimplementedProjectServiceHandler
	console *FakeConsole
}

func (p fakeProjects) List(context.Context, *consolev1.ListProjectsRequest) (*consolev1.ListProjectsResponse, error) {
	f := p.console
	f.mu.Lock()
	defer f.mu.Unlock()
	return &consolev1.ListProjectsResponse{Projects: f.Projects}, nil
}

func (p fakeProjects) Create(_ context.Context, req *consolev1.CreateProjectRequest) (*consolev1.CreateProjectResponse, error) {
	f := p.console
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Created = append(f.Created, req)
	created := &consolev1.Project{Id: "0199c3a2-5b7e-7c4d-8a1f-000000000001", OrganizationId: "org_1", Name: req.GetName(), Slug: req.GetSlug()}
	f.Projects = append(f.Projects, created)
	return &consolev1.CreateProjectResponse{Project: created}, nil
}

type fakeEndpoints struct {
	consolev1connect.UnimplementedEndpointServiceHandler
	console *FakeConsole
}

func (e fakeEndpoints) ResolveRunner(_ context.Context, req *consolev1.ResolveRunnerRequest) (*consolev1.ResolveRunnerResponse, error) {
	f := e.console
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Resolved = append(f.Resolved, req)
	if bound := f.Bindings[req.GetTier()]; bound != nil {
		return proto.CloneOf(bound), nil
	}
	if req.GetProvider() != "" {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("bind a runner to %s", req.GetTier()))
	}
	return &consolev1.ResolveRunnerResponse{Runner: &consolev1.Endpoint{
		Id: console.CloudEndpointID, Name: console.CloudEndpointID, Url: proto.String(f.Runner.URL), Version: proto.String(f.RunnerVersion),
	}}, nil
}

type fakeJobService struct {
	consolev1connect.UnimplementedJobServiceHandler
	console *FakeConsole
}

func (j fakeJobService) Dispatch(_ context.Context, req *consolev1.DispatchJobRequest) (*consolev1.DispatchJobResponse, error) {
	f := j.console
	f.mu.Lock()
	f.DispatchTries = append(f.DispatchTries, req.GetRequestId())
	if len(f.DispatchFails) > 0 {
		failure := f.DispatchFails[0]
		f.DispatchFails = f.DispatchFails[1:]
		f.mu.Unlock()
		return nil, failure
	}
	tenant := "org_1"
	for _, known := range f.Projects {
		if known.GetId() == req.GetProjectId() {
			tenant = known.GetOrganizationId()
		}
	}
	f.mu.Unlock()
	started, err := f.Runner.start(tenant, req)
	if err != nil {
		return nil, err
	}
	f.mu.Lock()
	f.Dispatched = append(f.Dispatched, req)
	f.mu.Unlock()
	return &consolev1.DispatchJobResponse{Job: &consolev1.DispatchedJob{
		Id: started.GetId(), ProjectId: req.GetProjectId(), Tier: req.GetTier(), RunnerId: req.GetRunnerId(), DeploymentId: started.GetDeploymentId(),
		Command: started.GetCommand(), State: runnerv1.JobState_JOB_STATE_QUEUED, CreatedAt: started.GetCreatedAt(),
	}}, nil
}

func (j fakeJobService) Get(_ context.Context, req *consolev1.GetJobRequest) (*consolev1.GetJobResponse, error) {
	f := j.console
	f.mu.Lock()
	refusal := f.GetRefusal
	f.mu.Unlock()
	if refusal != nil {
		return nil, refusal
	}
	started, found := f.Runner.startedJob(req.GetJobId())
	if !found {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("no job %s", req.GetJobId()))
	}
	return &consolev1.GetJobResponse{Job: &consolev1.DispatchedJob{Id: req.GetJobId(), ProjectId: started.GetProjectId(), Tier: started.GetTier(), State: runnerv1.JobState_JOB_STATE_QUEUED}}, nil
}
