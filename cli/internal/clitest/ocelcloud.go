package clitest

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/cli/internal/cloud"
	"github.com/ocelhq/ocel/cli/internal/commands"
	"github.com/ocelhq/ocel/cli/internal/console"
	"github.com/ocelhq/ocel/cli/internal/version"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	"github.com/ocelhq/ocel/pkg/proto/cloud/v1/cloudv1connect"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	"github.com/ocelhq/ocel/pkg/proto/console/v1/consolev1connect"
)

const (
	FakeCloudToken    = "cloud-token"
	FakeCloudSourceID = "5f1c0a3e9b7d2c4f6a8e0b1d3c5f7a9e2b4d6f8a0c2e4b6d8f0a2c4e6b8d0f2a"
	FakeCloudJobID    = "job_fake"

	otherOrganizationCloudToken = "cloud-token-org-2"
)

var cloudTenants = map[string]string{FakeCloudToken: "org_1", otherOrganizationCloudToken: "org_2"}

var versionedProcedures = []string{
	cloudv1connect.UploadServiceMissingProcedure,
	cloudv1connect.UploadServiceCommitSourceProcedure,
	cloudv1connect.JobServiceStartJobProcedure,
}

type FakeOcelCloud struct {
	ConsoleURL  string
	APIURL      string
	ProviderURL string

	consolev1connect.UnimplementedProjectServiceHandler
	consolev1connect.UnimplementedConsoleServiceHandler
	consolev1connect.UnimplementedDeploymentServiceHandler
	cloudv1connect.UnimplementedUploadServiceHandler
	cloudv1connect.UnimplementedJobServiceHandler

	mu            sync.Mutex
	Organizations []console.Organization
	ActiveOrg     string
	Projects      []*consolev1.Project
	Created       []*consolev1.CreateProjectRequest
	ActivatedOrgs []string
	TokenOrgs     []string
	TokenScopes   [][]string
	Committed     *cloudv1.SourceManifest
	CloudBearers  []string
	Started       []*cloudv1.StartJobRequest
	JobReads      []*cloudv1.Job
	OnJobRead     func(*cloudv1.Job)
	Canceled      []string
	Events        []*consolev1.RecordEnvironmentEventRequest
	Reports       []*consolev1.ReportRequest

	blobs   map[string][]byte
	sources map[string]bool
	jobs    map[string]*fakeJob
}

type fakeJob struct {
	tenant string
	job    *cloudv1.Job
	events []*cloudv1.JobEvent
}

type tenantKey struct{}

func ServeOcelCloud(t *testing.T) *FakeOcelCloud {
	t.Helper()
	fake := &FakeOcelCloud{
		Organizations: []console.Organization{{ID: "org_1", Name: "Acme Inc", Slug: "acme"}},
		JobReads:      []*cloudv1.Job{{State: cloudv1.JobState_JOB_STATE_SUCCEEDED}},
		blobs:         map[string][]byte{},
		sources:       map[string]bool{},
		jobs:          map[string]*fakeJob{},
	}
	validate := connect.WithInterceptors(connectvalidate.NewInterceptor())

	consoleMux := http.NewServeMux()
	consoleMux.HandleFunc("/api/auth/organization/list", func(w http.ResponseWriter, _ *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		_ = json.NewEncoder(w).Encode(fake.Organizations)
	})
	consoleMux.HandleFunc("/api/auth/get-session", func(w http.ResponseWriter, _ *http.Request) {
		fake.mu.Lock()
		defer fake.mu.Unlock()
		_, _ = fmt.Fprintf(w, `{"session":{"activeOrganizationId":%q},"user":{"email":"dev@acme.test"}}`, fake.ActiveOrg)
	})
	consoleMux.HandleFunc("/api/auth/organization/set-active", func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			OrganizationID string `json:"organizationId"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		fake.mu.Lock()
		fake.ActivatedOrgs = append(fake.ActivatedOrgs, body.OrganizationID)
		fake.mu.Unlock()
		_, _ = w.Write([]byte("{}"))
	})
	for _, mount := range []func() (string, http.Handler){
		func() (string, http.Handler) { return consolev1connect.NewProjectServiceHandler(fake, validate) },
		func() (string, http.Handler) { return consolev1connect.NewConsoleServiceHandler(fake, validate) },
		func() (string, http.Handler) { return consolev1connect.NewDeploymentServiceHandler(fake, validate) },
	} {
		path, handler := mount()
		consoleMux.Handle("/api/connect"+path, http.StripPrefix("/api/connect", handler))
	}
	consoleServer := httptest.NewServer(consoleMux)
	t.Cleanup(consoleServer.Close)
	fake.ConsoleURL = consoleServer.URL

	cloudOptions := connect.WithInterceptors(connectvalidate.NewInterceptor(), fake.authenticate())
	cloudMux := http.NewServeMux()
	cloudMux.Handle(cloudv1connect.NewUploadServiceHandler(fake, cloudOptions))
	cloudMux.Handle(cloudv1connect.NewJobServiceHandler(fake, cloudOptions))
	cloudMux.HandleFunc("PUT /v1/blobs/{digest}", fake.putBlob)
	cloudServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.CloudBearers = append(fake.CloudBearers, r.Header.Get("Authorization"))
		fake.mu.Unlock()
		cloudMux.ServeHTTP(w, r)
	}))
	t.Cleanup(cloudServer.Close)
	fake.APIURL = cloudServer.URL
	return fake
}

func (f *FakeOcelCloud) LogIn(invocation *commands.Invocation) {
	invocation.Console.LoadCredentials = func() (console.Credentials, error) {
		return console.Credentials{AccessToken: "device-session", APIURL: f.ConsoleURL, Email: "dev@acme.test"}, nil
	}
	invocation.CloudOffer = func(context.Context, console.Credentials) (*console.CloudOffer, error) {
		return &console.CloudOffer{APIURL: f.APIURL, ProviderURL: f.ProviderURL, Audience: "ocel-cloud"}, nil
	}
}

func (f *FakeOcelCloud) List(context.Context, *consolev1.ListProjectsRequest) (*consolev1.ListProjectsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return &consolev1.ListProjectsResponse{Projects: f.Projects}, nil
}

func (f *FakeOcelCloud) Create(_ context.Context, req *consolev1.CreateProjectRequest) (*consolev1.CreateProjectResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Created = append(f.Created, req)
	created := &consolev1.Project{Id: "0199c3a2-5b7e-7c4d-8a1f-000000000001", OrganizationId: "org_1", Name: req.GetName(), Slug: req.GetSlug()}
	f.Projects = append(f.Projects, created)
	return &consolev1.CreateProjectResponse{Project: created}, nil
}

func (f *FakeOcelCloud) RecordEnvironmentEvent(_ context.Context, req *consolev1.RecordEnvironmentEventRequest) (*consolev1.RecordEnvironmentEventResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Events = append(f.Events, req)
	return &consolev1.RecordEnvironmentEventResponse{}, nil
}

func (f *FakeOcelCloud) Report(_ context.Context, req *consolev1.ReportRequest) (*consolev1.ReportResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Reports = append(f.Reports, req)
	return &consolev1.ReportResponse{}, nil
}

func (f *FakeOcelCloud) ReportedDeployments() []*consolev1.ReportRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*consolev1.ReportRequest(nil), f.Reports...)
}

func (f *FakeOcelCloud) Link(t *testing.T, dir string) {
	t.Helper()
	if err := console.WriteLink(dir, console.Link{APIURL: f.ConsoleURL, OrganizationID: "org_1", ProjectID: FixtureConsoleProjectID, ProjectName: FixtureSlug}); err != nil {
		t.Fatal(err)
	}
}

func (f *FakeOcelCloud) RecordedEvents() []*consolev1.RecordEnvironmentEventRequest {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]*consolev1.RecordEnvironmentEventRequest(nil), f.Events...)
}

func (f *FakeOcelCloud) IssueCloudToken(_ context.Context, req *consolev1.IssueCloudTokenRequest) (*consolev1.IssueCloudTokenResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.TokenOrgs = append(f.TokenOrgs, req.GetOrganizationId())
	f.TokenScopes = append(f.TokenScopes, req.GetScopes())
	return &consolev1.IssueCloudTokenResponse{Token: FakeCloudToken, ExpiresAt: timestamppb.New(time.Now().Add(time.Hour))}, nil
}

func (f *FakeOcelCloud) authenticate() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			tenant, known := tenantOf(req.Header().Get("Authorization"))
			if !known {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("the bearer token is no organization's"))
			}
			if slices.Contains(versionedProcedures, req.Spec().Procedure) {
				if err := refuseUnservedVersion(req.Header().Get(cloud.VersionHeader)); err != nil {
					return nil, err
				}
			}
			return next(context.WithValue(ctx, tenantKey{}, tenant), req)
		}
	}
}

func tenantOf(authorization string) (string, bool) {
	token, bearer := strings.CutPrefix(authorization, "Bearer ")
	tenant, known := cloudTenants[token]
	return tenant, bearer && known
}

func refuseUnservedVersion(asked string) error {
	if asked == version.Version {
		return nil
	}
	refused := connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("this Ocel Cloud serves ocel %s, not %q", version.Version, asked))
	detail, err := connect.NewErrorDetail(&cloudv1.VersionUnserved{Served: []string{version.Version}})
	if err != nil {
		return err
	}
	refused.AddDetail(detail)
	return refused
}

func tenantIn(ctx context.Context) string {
	return ctx.Value(tenantKey{}).(string)
}

func (f *FakeOcelCloud) Missing(ctx context.Context, req *cloudv1.MissingRequest) (*cloudv1.MissingResponse, error) {
	tenant := tenantIn(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	var missing []string
	for _, digest := range req.GetDigests() {
		if _, held := f.blobs[tenant+"/"+digest]; !held {
			missing = append(missing, digest)
		}
	}
	return &cloudv1.MissingResponse{Digests: missing}, nil
}

func (f *FakeOcelCloud) CommitSource(ctx context.Context, req *cloudv1.CommitSourceRequest) (*cloudv1.CommitSourceResponse, error) {
	tenant := tenantIn(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, file := range req.GetManifest().GetFiles() {
		if _, held := f.blobs[tenant+"/"+file.GetDigest()]; file.GetDigest() != "" && !held {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s was never uploaded", file.GetPath()))
		}
	}
	f.Committed = req.GetManifest()
	f.sources[tenant+"/"+FakeCloudSourceID] = true
	return &cloudv1.CommitSourceResponse{SourceId: FakeCloudSourceID}, nil
}

func (f *FakeOcelCloud) StartJob(ctx context.Context, req *cloudv1.StartJobRequest) (*cloudv1.StartJobResponse, error) {
	tenant := tenantIn(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.sources[tenant+"/"+req.GetSourceId()] {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("your organization committed no source %s", req.GetSourceId()))
	}
	f.Started = append(f.Started, req)
	id := FakeCloudJobID
	if len(f.jobs) > 0 {
		id = fmt.Sprintf("%s_%d", FakeCloudJobID, len(f.jobs)+1)
	}
	now := timestamppb.Now()
	job := &cloudv1.Job{Id: id, ProjectId: req.GetProjectId(), Command: req.GetCommand(), State: cloudv1.JobState_JOB_STATE_RUNNING, SourceId: req.GetSourceId(), CreatedAt: now, StartedAt: now}
	f.jobs[id] = &fakeJob{tenant: tenant, job: job}
	return &cloudv1.StartJobResponse{Job: proto.CloneOf(job)}, nil
}

func (f *FakeOcelCloud) ownedJob(ctx context.Context, id string) (*fakeJob, error) {
	owned, found := f.jobs[id]
	if !found || owned.tenant != tenantIn(ctx) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("your organization has no job %s", id))
	}
	return owned, nil
}

func (f *FakeOcelCloud) GetJob(ctx context.Context, req *cloudv1.GetJobRequest) (*cloudv1.GetJobResponse, error) {
	f.mu.Lock()
	owned, err := f.ownedJob(ctx, req.GetJobId())
	if err != nil {
		f.mu.Unlock()
		return nil, err
	}
	if len(f.JobReads) > 0 {
		owned.job = proto.CloneOf(f.JobReads[0])
		owned.job.Id = req.GetJobId()
		if len(f.JobReads) > 1 {
			f.JobReads = f.JobReads[1:]
		}
	}
	read := proto.CloneOf(owned.job)
	onRead := f.OnJobRead
	f.mu.Unlock()
	if onRead != nil {
		onRead(read)
	}
	return &cloudv1.GetJobResponse{Job: read}, nil
}

func (f *FakeOcelCloud) CancelJob(ctx context.Context, req *cloudv1.CancelJobRequest) (*cloudv1.CancelJobResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	owned, err := f.ownedJob(ctx, req.GetJobId())
	if err != nil {
		return nil, err
	}
	f.Canceled = append(f.Canceled, req.GetJobId())
	if !cloud.IsJobEnded(owned.job.GetState()) {
		owned.job.State = cloudv1.JobState_JOB_STATE_CANCELED
		owned.job.Input = nil
		owned.job.EndedAt = timestamppb.Now()
	}
	return &cloudv1.CancelJobResponse{Job: proto.CloneOf(owned.job)}, nil
}

func (f *FakeOcelCloud) ListJobEvents(ctx context.Context, req *cloudv1.ListJobEventsRequest) (*cloudv1.ListJobEventsResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	owned, err := f.ownedJob(ctx, req.GetJobId())
	if err != nil {
		return nil, err
	}
	later := owned.events[min(req.GetAfterSeq(), uint64(len(owned.events))):]
	limit := len(later)
	if req.GetLimit() > 0 {
		limit = min(limit, int(req.GetLimit()))
	}
	page := make([]*cloudv1.JobEvent, 0, limit)
	for _, event := range later[:limit] {
		page = append(page, proto.CloneOf(event))
	}
	return &cloudv1.ListJobEventsResponse{Events: page, More: limit < len(later), Job: proto.CloneOf(owned.job)}, nil
}

func (f *FakeOcelCloud) RespondJob(ctx context.Context, req *cloudv1.RespondJobRequest) (*cloudv1.RespondJobResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	owned, err := f.ownedJob(ctx, req.GetJobId())
	if err != nil {
		return nil, err
	}
	if owned.job.GetState() != cloudv1.JobState_JOB_STATE_INPUT_REQUIRED || owned.job.GetInput().GetId() != req.GetInputId() {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("job %s waits on no input %s", req.GetJobId(), req.GetInputId()))
	}
	owned.job.State = cloudv1.JobState_JOB_STATE_RUNNING
	owned.job.Input = nil
	return &cloudv1.RespondJobResponse{Job: proto.CloneOf(owned.job)}, nil
}

func (f *FakeOcelCloud) putBlob(w http.ResponseWriter, r *http.Request) {
	tenant, known := tenantOf(r.Header.Get("Authorization"))
	if !known {
		http.Error(w, "the bearer token is no organization's", http.StatusUnauthorized)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	sum := sha256.Sum256(body)
	if hex.EncodeToString(sum[:]) != r.PathValue("digest") {
		http.Error(w, "the body does not hash to the digest", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blobs[tenant+"/"+r.PathValue("digest")] = body
	w.WriteHeader(http.StatusNoContent)
}

func (f *FakeOcelCloud) CommittedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var paths []string
	for _, file := range f.Committed.GetFiles() {
		paths = append(paths, file.GetPath())
	}
	return paths
}
