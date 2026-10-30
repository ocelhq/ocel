package clitest

import (
	"context"
	"crypto/hmac"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/cli/internal/runnerclient"
	consolev1 "github.com/ocelhq/ocel/pkg/proto/console/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/proto/runner/v1/runnerv1connect"
)

const (
	FakeCloudToken    = "cloud-token"
	FakeCloudSourceID = "5f1c0a3e9b7d2c4f6a8e0b1d3c5f7a9e2b4d6f8a0c2e4b6d8f0a2c4e6b8d0f2a"
	FakeJobID         = "job_fake"
	FakeRunnerID      = "0199c3a2-5b7e-7c4d-8a1f-0000000000aa"
	FakeDeploymentID  = "4bf92f3577b34da6a3ce929d0e0e4736"

	otherOrganizationCloudToken = "cloud-token-org-2"
)

var cloudTenants = map[string]string{FakeCloudToken: "org_1", otherOrganizationCloudToken: "org_2"}

type FakeRunner struct {
	runnerv1connect.UnimplementedRunnerServiceHandler

	URL          string
	StorageURL   string
	UploadMethod string

	mu        sync.Mutex
	Committed *runnerv1.SourceManifest
	Bearers   []string
	JobReads  []*runnerv1.Job
	OnJobRead func(*runnerv1.Job)
	JobEvents []*runnerv1.JobEvent
	Unaware   int
	Canceled  []string
	Answered  []string
	Supplied  []*runnerv1.SupplyVariablesRequest

	blobs      map[string][]byte
	sources    map[string]bool
	jobs       map[string]*fakeJob
	signingKey []byte
}

type fakeJob struct {
	tenant  string
	job     *runnerv1.Job
	events  []*runnerv1.JobEvent
	unaware int
}

type tenantKey struct{}

func ServeRunner(t *testing.T) *FakeRunner {
	t.Helper()
	runner := &FakeRunner{
		JobReads:     []*runnerv1.Job{{State: runnerv1.JobState_JOB_STATE_SUCCEEDED}},
		UploadMethod: http.MethodPut,
		blobs:        map[string][]byte{},
		sources:      map[string]bool{},
		jobs:         map[string]*fakeJob{},
		signingKey:   []byte(rand.Text()),
	}
	mux := http.NewServeMux()
	mux.Handle(runnerv1connect.NewRunnerServiceHandler(runner, connect.WithInterceptors(connectvalidate.NewInterceptor(), runner.authenticate())))
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		runner.mu.Lock()
		runner.Bearers = append(runner.Bearers, r.Header.Get("Authorization"))
		runner.mu.Unlock()
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	runner.URL = server.URL

	storage := http.NewServeMux()
	storage.HandleFunc("PUT /blobs/{tenant}/{digest}", runner.putBlob)
	storage.HandleFunc("POST /blobs", runner.postBlob)
	storageServer := httptest.NewServer(storage)
	t.Cleanup(storageServer.Close)
	runner.StorageURL = storageServer.URL
	return runner
}

func (f *FakeRunner) start(tenant string, req *consolev1.DispatchJobRequest) (*runnerv1.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if !f.sources[tenant+"/"+req.GetJob().GetSourceId()] {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("your organization committed no source %s", req.GetJob().GetSourceId()))
	}
	id := FakeJobID
	if len(f.jobs) > 0 {
		id = fmt.Sprintf("%s_%d", FakeJobID, len(f.jobs)+1)
	}
	now := timestamppb.Now()
	job := &runnerv1.Job{Id: id, ProjectId: req.GetProjectId(), Tier: req.GetTier(), Command: req.GetJob().GetCommand(), State: runnerv1.JobState_JOB_STATE_RUNNING, CreatedAt: now, StartedAt: now}
	if command := req.GetJob().GetCommand(); command == runnerv1.JobCommand_JOB_COMMAND_DEPLOY || command == runnerv1.JobCommand_JOB_COMMAND_PREVIEW_UP {
		job.DeploymentId = FakeDeploymentID
	}
	events := make([]*runnerv1.JobEvent, 0, len(f.JobEvents))
	for _, event := range f.JobEvents {
		events = append(events, proto.CloneOf(event))
	}
	f.jobs[id] = &fakeJob{tenant: tenant, job: job, events: events, unaware: f.Unaware}
	return proto.CloneOf(job), nil
}

func (f *FakeRunner) startedJob(id string) (*runnerv1.Job, bool) {
	f.mu.Lock()
	defer f.mu.Unlock()
	owned, found := f.jobs[id]
	if !found {
		return nil, false
	}
	return proto.CloneOf(owned.job), true
}

func (f *FakeRunner) authenticate() connect.UnaryInterceptorFunc {
	return func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			bearer, isBearer := readBearer(req.Header().Get("Authorization"))
			tenant, known := cloudTenants[bearer.token]
			if !isBearer || !known {
				return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("the bearer token is no organization's"))
			}
			if err := bearer.refuseUnheld(req.Spec().Procedure); err != nil {
				return nil, err
			}
			return next(context.WithValue(ctx, tenantKey{}, tenant), req)
		}
	}
}

func tenantIn(ctx context.Context) string {
	return ctx.Value(tenantKey{}).(string)
}

func (f *FakeRunner) ListMissingBlobs(ctx context.Context, req *runnerv1.ListMissingBlobsRequest) (*runnerv1.ListMissingBlobsResponse, error) {
	tenant := tenantIn(ctx)
	f.mu.Lock()
	defer f.mu.Unlock()
	missing := &runnerv1.ListMissingBlobsResponse{}
	for _, digest := range req.GetDigests() {
		if _, held := f.blobs[tenant+"/"+digest]; !held {
			missing.Uploads = append(missing.Uploads, f.uploadTarget(tenant, digest))
		}
	}
	return missing, nil
}

func (f *FakeRunner) uploadTarget(tenant, digest string) *runnerv1.Upload {
	key := tenant + "/" + digest
	if f.UploadMethod == http.MethodPost {
		return &runnerv1.Upload{
			Digest: digest,
			Url:    f.StorageURL + "/blobs",
			Method: http.MethodPost,
			Fields: map[string]string{"key": key, "signature": f.signatureOf(http.MethodPost, key)},
		}
	}
	return &runnerv1.Upload{
		Digest:  digest,
		Url:     f.StorageURL + "/blobs/" + key + "?signature=" + f.signatureOf(http.MethodPut, key),
		Method:  http.MethodPut,
		Headers: map[string]string{"Content-Type": "application/octet-stream"},
	}
}

func (f *FakeRunner) signatureOf(method, key string) string {
	mac := hmac.New(sha256.New, f.signingKey)
	_, _ = io.WriteString(mac, method+" "+key)
	return hex.EncodeToString(mac.Sum(nil))
}

func (f *FakeRunner) CommitSource(ctx context.Context, req *runnerv1.CommitSourceRequest) (*runnerv1.CommitSourceResponse, error) {
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
	return &runnerv1.CommitSourceResponse{SourceId: FakeCloudSourceID}, nil
}

func (f *FakeRunner) ownedJob(ctx context.Context, id string) (*fakeJob, error) {
	owned, found := f.jobs[id]
	if !found || owned.tenant != tenantIn(ctx) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("your organization has no job %s", id))
	}
	return owned, nil
}

func (f *FakeRunner) readJob(owned *fakeJob, id string) *runnerv1.Job {
	if len(f.JobReads) > 0 {
		owned.job = proto.CloneOf(f.JobReads[0])
		owned.job.Id = id
		if len(f.JobReads) > 1 {
			f.JobReads = f.JobReads[1:]
		}
	}
	return proto.CloneOf(owned.job)
}

func (f *FakeRunner) CancelJob(ctx context.Context, req *runnerv1.CancelJobRequest) (*runnerv1.CancelJobResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	owned, err := f.ownedJob(ctx, req.GetJobId())
	if err != nil {
		return nil, err
	}
	f.Canceled = append(f.Canceled, req.GetJobId())
	if !runnerclient.IsJobEnded(owned.job.GetState()) {
		owned.job.State = runnerv1.JobState_JOB_STATE_CANCELED
		owned.job.Input = nil
		owned.job.EndedAt = timestamppb.Now()
	}
	return &runnerv1.CancelJobResponse{Job: proto.CloneOf(owned.job)}, nil
}

func (f *FakeRunner) ListJobEvents(ctx context.Context, req *runnerv1.ListJobEventsRequest) (*runnerv1.ListJobEventsResponse, error) {
	f.mu.Lock()
	owned, err := f.ownedJob(ctx, req.GetJobId())
	if err == nil && owned.unaware > 0 {
		owned.unaware--
		err = connect.NewError(connect.CodeNotFound, fmt.Errorf("this runner has not taken job %s yet", req.GetJobId()))
	}
	if err != nil {
		f.mu.Unlock()
		return nil, err
	}
	var later []*runnerv1.JobEvent
	for _, event := range owned.events {
		if event.GetSeq() > req.GetAfterSeq() {
			later = append(later, proto.CloneOf(event))
		}
	}
	read := f.readJob(owned, req.GetJobId())
	onRead := f.OnJobRead
	f.mu.Unlock()
	if onRead != nil {
		onRead(read)
	}
	return &runnerv1.ListJobEventsResponse{Events: later, Job: read}, nil
}

func (f *FakeRunner) AnswerConfirmation(ctx context.Context, req *runnerv1.AnswerConfirmationRequest) (*runnerv1.AnswerConfirmationResponse, error) {
	job, err := f.answer(ctx, req.GetJobId(), req.GetInputId())
	if err != nil {
		return nil, err
	}
	return &runnerv1.AnswerConfirmationResponse{Job: job}, nil
}

func (f *FakeRunner) SupplyVariables(ctx context.Context, req *runnerv1.SupplyVariablesRequest) (*runnerv1.SupplyVariablesResponse, error) {
	f.mu.Lock()
	f.Supplied = append(f.Supplied, req)
	f.mu.Unlock()
	job, err := f.answer(ctx, req.GetJobId(), req.GetInputId())
	if err != nil {
		return nil, err
	}
	return &runnerv1.SupplyVariablesResponse{Job: job}, nil
}

func (f *FakeRunner) answer(ctx context.Context, jobID, inputID string) (*runnerv1.Job, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.Answered = append(f.Answered, inputID)
	owned, err := f.ownedJob(ctx, jobID)
	if err != nil {
		return nil, err
	}
	if owned.job.GetState() != runnerv1.JobState_JOB_STATE_INPUT_REQUIRED || owned.job.GetInput().GetId() != inputID {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("job %s waits on no input %s", jobID, inputID))
	}
	owned.job.State = runnerv1.JobState_JOB_STATE_RUNNING
	owned.job.Input = nil
	return proto.CloneOf(owned.job), nil
}

func (f *FakeRunner) putBlob(w http.ResponseWriter, r *http.Request) {
	key := r.PathValue("tenant") + "/" + r.PathValue("digest")
	if !hmac.Equal([]byte(r.URL.Query().Get("signature")), []byte(f.signatureOf(http.MethodPut, key))) {
		http.Error(w, "the signature does not match", http.StatusForbidden)
		return
	}
	if r.Header.Get("Content-Type") != "application/octet-stream" {
		http.Error(w, "the content type is not the one signed", http.StatusForbidden)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.keepBlob(w, key, body)
}

func (f *FakeRunner) postBlob(w http.ResponseWriter, r *http.Request) {
	form, err := r.MultipartReader()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	fields := map[string]string{}
	for {
		part, err := form.NextPart()
		if err != nil {
			http.Error(w, "the form ends before its file", http.StatusBadRequest)
			return
		}
		value, err := io.ReadAll(part)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if part.FormName() != "file" {
			fields[part.FormName()] = string(value)
			continue
		}
		if !hmac.Equal([]byte(fields["signature"]), []byte(f.signatureOf(http.MethodPost, fields["key"]))) {
			http.Error(w, "the policy signature does not match", http.StatusForbidden)
			return
		}
		f.keepBlob(w, fields["key"], value)
		return
	}
}

func (f *FakeRunner) keepBlob(w http.ResponseWriter, key string, body []byte) {
	sum := sha256.Sum256(body)
	if _, digest, _ := strings.Cut(key, "/"); hex.EncodeToString(sum[:]) != digest {
		http.Error(w, "the body does not hash to the digest", http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.blobs[key] = body
	w.WriteHeader(http.StatusNoContent)
}

func (f *FakeRunner) CommittedPaths() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var paths []string
	for _, file := range f.Committed.GetFiles() {
		paths = append(paths, file.GetPath())
	}
	return paths
}
