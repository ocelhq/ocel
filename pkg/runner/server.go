package runner

import (
	"cmp"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"connectrpc.com/connect"
	"connectrpc.com/validate"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/proto/runner/v1/runnerv1connect"
	"github.com/ocelhq/ocel/pkg/scope"
)

const MaxSourceBytes = 2 << 30

const (
	defaultMaxJobs = 4
	uploadLife     = time.Hour
	launchGrace    = 10 * time.Minute
	beatGrace      = 2 * time.Minute
	retention      = 24 * time.Hour
	answerWait     = 50 * time.Second
	cancelGrace    = time.Minute
	launchTimeout  = time.Minute
)

type VariableValues struct {
	OrganizationID string
	ProjectID      string
	Slug           string
	Tier           environmentv1.Tier
	Values         []*runnerv1.VariableValue
}

type Config struct {
	ID             string
	Dir            string
	URL            string
	Launcher       Launcher
	StoreVariables func(ctx context.Context, values VariableValues) error
	MaxJobs        int
	CancelGrace    time.Duration
	Now            func() time.Time
}

type Server struct {
	id             string
	dir            string
	launcher       Launcher
	storeVariables func(ctx context.Context, values VariableValues) error
	maxJobs        int
	cancelGrace    time.Duration
	now            func() time.Time
	blobs          *blobs

	mu     sync.Mutex
	jobs   map[jobKey]*heldJob
	done   chan struct{}
	closed sync.Once
}

func New(config Config) (*Server, error) {
	switch {
	case config.ID == "":
		return nil, errors.New("runner: the config names no runner id, which job tokens are checked against")
	case config.Dir == "":
		return nil, errors.New("runner: the config names no directory to keep jobs and sources in")
	case config.URL == "":
		return nil, errors.New("runner: the config names no URL to sign uploads under")
	case config.Launcher == nil:
		return nil, errors.New("runner: the config has no launcher to start jobs with")
	}
	now := config.Now
	if now == nil {
		now = time.Now
	}
	stored, err := openBlobs(filepath.Join(config.Dir, "blobs"), config.URL, now)
	if err != nil {
		return nil, fmt.Errorf("runner: %w", err)
	}
	s := &Server{
		id: config.ID, dir: config.Dir, launcher: config.Launcher,
		storeVariables: config.StoreVariables, maxJobs: cmp.Or(config.MaxJobs, defaultMaxJobs), cancelGrace: cmp.Or(config.CancelGrace, cancelGrace),
		now: now, blobs: stored, jobs: map[jobKey]*heldJob{}, done: make(chan struct{}),
	}
	if err := s.loadJobs(); err != nil {
		return nil, fmt.Errorf("runner: %w", err)
	}
	return s, nil
}

func (s *Server) Close() { s.closed.Do(func() { close(s.done) }) }

func (s *Server) RunnerServiceHandler() (string, http.Handler) {
	return runnerv1connect.NewRunnerServiceHandler(s, connect.WithInterceptors(validate.NewInterceptor()))
}

func (s *Server) JobAgentServiceHandler() (string, http.Handler) {
	return runnerv1connect.NewJobAgentServiceHandler(&jobAgentService{s}, connect.WithInterceptors(validate.NewInterceptor()))
}

func (s *Server) BlobsHandler() (string, http.Handler) { return BlobsPath, s.blobs }

func (s *Server) person(ctx context.Context) (endpoint.Caller, error) {
	caller, ok := endpoint.CallerOf(ctx)
	if !ok {
		return endpoint.Caller{}, connect.NewError(connect.CodeUnauthenticated, errors.New("the call bears no token this runner verified"))
	}
	return caller, nil
}

func (s *Server) refuseRoleBelowCommand(caller endpoint.Caller, key jobKey) error {
	job, err := s.read(key)
	if err != nil {
		return err
	}
	if least := scope.LeastRole(job.GetCommand()); !caller.HoldsRole(least) {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("acting on a %s job needs the %s role or above in the organization", commandName(job.GetCommand()), scope.ClaimOf(least)))
	}
	return nil
}

func (s *Server) jobCaller(ctx context.Context) (endpoint.Caller, endpoint.Job, error) {
	caller, _ := endpoint.CallerOf(ctx)
	job, ok := caller.Job()
	if !ok {
		return endpoint.Caller{}, endpoint.Job{}, connect.NewError(connect.CodeUnauthenticated, errors.New("the call bears no job token this runner verified"))
	}
	if !jobIDPattern(job.ID) {
		return endpoint.Caller{}, endpoint.Job{}, connect.NewError(connect.CodePermissionDenied, errors.New("the job token names no job id a runner keeps"))
	}
	return caller, job, nil
}

func jobIDPattern(id string) bool {
	if id == "" || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if (r < 'a' || r > 'z') && (r < 'A' || r > 'Z') && (r < '0' || r > '9') && r != '_' && r != '-' {
			return false
		}
	}
	return true
}

func (s *Server) ListMissingBlobs(ctx context.Context, request *runnerv1.ListMissingBlobsRequest) (*runnerv1.ListMissingBlobsResponse, error) {
	caller, err := s.person(ctx)
	if err != nil {
		return nil, err
	}
	response := &runnerv1.ListMissingBlobsResponse{}
	for _, digest := range request.GetDigests() {
		if s.blobs.has(caller.OrganizationID, digest) {
			s.blobs.touch(caller.OrganizationID, digest)
			continue
		}
		response.Uploads = append(response.Uploads, &runnerv1.Upload{
			Digest: digest, Method: http.MethodPut,
			Url: s.blobs.sign(http.MethodPut, caller.OrganizationID, digest, uploadLife),
		})
	}
	return response, nil
}

func (s *Server) CommitSource(ctx context.Context, request *runnerv1.CommitSourceRequest) (*runnerv1.CommitSourceResponse, error) {
	caller, err := s.person(ctx)
	if err != nil {
		return nil, err
	}
	var total uint64
	for _, file := range request.GetManifest().GetFiles() {
		if file.GetSize() > MaxSourceBytes-total {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("the source holds more than 2 GiB, the most a source may hold"))
		}
		total += file.GetSize()
		if file.GetDigest() == "" {
			continue
		}
		size, ok := s.blobs.size(caller.OrganizationID, file.GetDigest())
		if !ok {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s names blob %s, which was never uploaded: upload it first", file.GetPath(), file.GetDigest()))
		}
		if size != file.GetSize() {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s says %d bytes, and its blob holds %d", file.GetPath(), file.GetSize(), size))
		}
	}
	encoded, err := proto.MarshalOptions{Deterministic: true}.Marshal(request.GetManifest())
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(encoded)
	id := hex.EncodeToString(sum[:])
	if err := writeFileAtomically(s.sourcePath(caller.OrganizationID, id), encoded, 0o600); err != nil {
		return nil, err
	}
	for _, file := range request.GetManifest().GetFiles() {
		if file.GetDigest() != "" {
			s.blobs.touch(caller.OrganizationID, file.GetDigest())
		}
	}
	return &runnerv1.CommitSourceResponse{SourceId: id}, nil
}

func (s *Server) sourcePath(organization, id string) string {
	return filepath.Join(s.dir, "sources", organizationDir(organization), id)
}

func (s *Server) source(organization, id string) (*runnerv1.SourceManifest, error) {
	encoded, err := os.ReadFile(s.sourcePath(organization, id))
	if errors.Is(err, fs.ErrNotExist) {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("your organization committed no source %s: upload and commit it first", id))
	}
	if err != nil {
		return nil, err
	}
	manifest := &runnerv1.SourceManifest{}
	return manifest, proto.Unmarshal(encoded, manifest)
}

func (s *Server) StartJob(ctx context.Context, request *runnerv1.StartJobRequest) (*runnerv1.StartJobResponse, error) {
	caller, token, err := s.jobCaller(ctx)
	if err != nil {
		return nil, err
	}
	key := jobKey{caller.OrganizationID, token.ID}
	now := s.now()
	if !token.Expires.After(now) {
		return nil, connect.NewError(connect.CodePermissionDenied, errors.New("the job token has expired"))
	}
	if err := RefuseOutsideToken(token, request.GetJob()); err != nil {
		return nil, err
	}
	bearer := bearerOf(ctx)
	if bearer == "" {
		return nil, connect.NewError(connect.CodeUnauthenticated, errors.New("the call bears no job token to hand the job's agent"))
	}
	if job, err := s.read(key); err == nil {
		return &runnerv1.StartJobResponse{Job: job}, nil
	}
	if id := request.GetJob().GetSourceId(); id != "" {
		if _, err := s.source(caller.OrganizationID, id); err != nil {
			return nil, err
		}
		_ = os.Chtimes(s.sourcePath(caller.OrganizationID, id), now, now)
	}

	s.mu.Lock()
	if h, ok := s.jobs[key]; ok {
		job := proto.CloneOf(h.job)
		s.mu.Unlock()
		return &runnerv1.StartJobResponse{Job: job}, nil
	}
	active := 0
	for _, h := range s.jobs {
		if h.holdsSlot() {
			active++
		}
	}
	if active >= s.maxJobs {
		s.mu.Unlock()
		return nil, connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("this runner is running %d jobs, the most it runs at once: start this one again when one of them ends", active))
	}
	h := &heldJob{
		record: record{Organization: caller.OrganizationID, Console: caller.Console, Token: token},
		job: &runnerv1.Job{
			Id: token.ID, ProjectId: token.ProjectID, Tier: tierProto(token.Tier), Command: request.GetJob().GetCommand(),
			DeploymentId: token.DeploymentID, State: runnerv1.JobState_JOB_STATE_QUEUED,
			Step: "Waiting for the job's agent", CreatedAt: timestamppb.New(now),
		},
		request: proto.CloneOf(request.GetJob()),
		changed: make(chan struct{}),
	}
	if err := s.save(key, h); err != nil {
		s.mu.Unlock()
		return nil, err
	}
	s.jobs[key] = h
	started := proto.CloneOf(h.job)
	s.mu.Unlock()

	launchCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), launchTimeout)
	defer cancel()
	handle, err := s.launcher.Launch(launchCtx, Launch{JobID: token.ID, OrganizationID: caller.OrganizationID, Token: bearer, Deadline: token.Expires})
	if err != nil {
		if forgetErr := s.forgetQueued(key); forgetErr != nil {
			err = errors.Join(err, forgetErr)
		}
		return nil, connect.NewError(connect.CodeUnavailable, fmt.Errorf("start the job's agent: %w", err))
	}
	if _, err := s.change(key, func(h *heldJob) (bool, error) {
		h.record.Handle = handle
		return true, nil
	}); err != nil {
		return nil, err
	}
	return &runnerv1.StartJobResponse{Job: started}, nil
}

func (s *Server) forgetQueued(key jobKey) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.jobs[key]
	if !ok || h.job.GetState() != runnerv1.JobState_JOB_STATE_QUEUED {
		return nil
	}
	delete(s.jobs, key)
	return os.RemoveAll(s.jobDir(key))
}

func RefuseOutsideToken(token endpoint.Job, request *runnerv1.JobRequest) error {
	denied := func(format string, args ...any) error {
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf(format, args...))
	}
	switch command := request.GetCommand(); command {
	case runnerv1.JobCommand_JOB_COMMAND_PREVIEW_UP, runnerv1.JobCommand_JOB_COMMAND_PREVIEW_RM:
		switch name := request.GetPreview().GetName(); {
		case token.Preview == "":
			return denied("the job token names no preview, and a %s job runs on one", commandName(command))
		case token.Preview != name:
			return denied("the job token is for preview %s, not %s", token.Preview, name)
		case token.Tier != environment.TierPreview:
			return denied("the job token is for the %s tier, and a %s job runs on the preview tier", token.Tier, commandName(command))
		}
	default:
		if token.Preview != "" {
			return denied("the job token names preview %s, and a %s job runs on no preview", token.Preview, commandName(command))
		}
		if command == runnerv1.JobCommand_JOB_COMMAND_DEPLOY && token.Tier != environment.TierProduction {
			return denied("the job token is for the %s tier, and a deploy job runs on production", token.Tier)
		}
	}
	return nil
}

func commandName(command runnerv1.JobCommand) string {
	return strings.ReplaceAll(strings.ToLower(strings.TrimPrefix(command.String(), "JOB_COMMAND_")), "_", " ")
}

func bearerOf(ctx context.Context) string {
	info, ok := connect.CallInfoForHandlerContext(ctx)
	if !ok {
		return ""
	}
	scheme, token, found := strings.Cut(info.RequestHeader().Get("Authorization"), " ")
	if !found || !strings.EqualFold(scheme, "Bearer") {
		return ""
	}
	return strings.TrimSpace(token)
}

func (s *Server) change(key jobKey, change func(h *heldJob) (bool, error)) (*heldJob, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.jobs[key]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("your organization has no job %s on this runner", key.id))
	}
	changed, err := change(h)
	if err != nil || !changed {
		h.events = nil
		return h, err
	}
	return h, s.save(key, h)
}

func (s *Server) read(key jobKey) (*runnerv1.Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	h, ok := s.jobs[key]
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("your organization has no job %s on this runner", key.id))
	}
	return proto.CloneOf(h.job), nil
}

func (s *Server) GetJob(ctx context.Context, request *runnerv1.GetJobRequest) (*runnerv1.GetJobResponse, error) {
	caller, err := s.person(ctx)
	if err != nil {
		return nil, err
	}
	job, err := s.read(jobKey{caller.OrganizationID, request.GetJobId()})
	if err != nil {
		return nil, err
	}
	return &runnerv1.GetJobResponse{Job: job}, nil
}

const defaultEventsLimit = 200

func (s *Server) ListJobEvents(ctx context.Context, request *runnerv1.ListJobEventsRequest) (*runnerv1.ListJobEventsResponse, error) {
	caller, err := s.person(ctx)
	if err != nil {
		return nil, err
	}
	key := jobKey{caller.OrganizationID, request.GetJobId()}
	s.mu.Lock()
	h, ok := s.jobs[key]
	var job *runnerv1.Job
	var kept uint64
	if ok {
		job, kept = proto.CloneOf(h.job), h.record.Events
	}
	s.mu.Unlock()
	if !ok {
		return nil, connect.NewError(connect.CodeNotFound, fmt.Errorf("your organization has no job %s on this runner", key.id))
	}
	limit := int(request.GetLimit())
	if limit == 0 {
		limit = defaultEventsLimit
	}
	events, err := s.readEvents(key, request.GetAfterSeq(), limit, kept)
	if err != nil {
		return nil, err
	}
	last := request.GetAfterSeq()
	if n := len(events); n > 0 {
		last = events[n-1].GetSeq()
	}
	return &runnerv1.ListJobEventsResponse{Events: events, More: last < kept, Job: job}, nil
}

func (s *Server) CancelJob(ctx context.Context, request *runnerv1.CancelJobRequest) (*runnerv1.CancelJobResponse, error) {
	caller, err := s.person(ctx)
	if err != nil {
		return nil, err
	}
	key := jobKey{caller.OrganizationID, request.GetJobId()}
	if err := s.refuseRoleBelowCommand(caller, key); err != nil {
		return nil, err
	}
	requested, queued := false, false
	if _, err := s.change(key, func(h *heldJob) (bool, error) {
		if ended(h.job.GetState()) || h.record.CancelRequested {
			return false, nil
		}
		h.record.CancelRequested, h.record.CancelRequestedAt = true, s.now()
		requested, queued = true, h.job.GetState() == runnerv1.JobState_JOB_STATE_QUEUED
		return true, nil
	}); err != nil {
		return nil, err
	}
	switch {
	case queued:
		s.forceCancel(ctx, key)
	case requested:
		go s.forceCancelAfter(key, s.cancelGrace)
	}
	job, err := s.read(key)
	if err != nil {
		return nil, err
	}
	return &runnerv1.CancelJobResponse{Job: job}, nil
}

func (s *Server) forceCancelAfter(key jobKey, grace time.Duration) {
	timer := time.NewTimer(grace)
	defer timer.Stop()
	select {
	case <-timer.C:
		ctx, cancel := context.WithTimeout(context.Background(), launchTimeout)
		defer cancel()
		s.forceCancel(ctx, key)
	case <-s.done:
	}
}

func (s *Server) forceCancel(ctx context.Context, key jobKey) {
	s.mu.Lock()
	h, ok := s.jobs[key]
	if !ok || ended(h.job.GetState()) {
		s.mu.Unlock()
		return
	}
	handle := h.record.Handle
	s.mu.Unlock()
	if err := s.launcher.Cancel(ctx, handle); err != nil {
		return
	}
	_, _ = s.change(key, func(h *heldJob) (bool, error) {
		if ended(h.job.GetState()) {
			return false, nil
		}
		h.end(s.now(), runnerv1.JobState_JOB_STATE_CANCELED, nil)
		return true, nil
	})
}

func tierProto(tier environment.Tier) environmentv1.Tier {
	switch tier {
	case environment.TierProduction:
		return environmentv1.Tier_TIER_PRODUCTION
	case environment.TierPreview:
		return environmentv1.Tier_TIER_PREVIEW
	}
	return environmentv1.Tier_TIER_UNSPECIFIED
}
