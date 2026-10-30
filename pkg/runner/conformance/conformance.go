package conformance

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect"

	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/proto/runner/v1/runnerv1connect"
	"github.com/ocelhq/ocel/pkg/tracing"
)

type Suite struct {
	URL               string
	Runner            string
	Organization      string
	OtherOrganization string
	PersonToken       func(t *testing.T, organization string, role scopev1.Role) string
	JobToken          func(t *testing.T, organization string, job endpoint.Job) string
}

var client = tracing.NewClient(2 * time.Minute)

const (
	settleTimeout = 30 * time.Second
	jobLife       = 30 * time.Minute
)

func Run(t *testing.T, suite Suite) {
	t.Helper()

	refuseIncompleteSuite(t, suite)
	r := runnerUnderTest{
		suite:  suite,
		mine:   newCaller(suite.URL, suite.PersonToken(t, suite.Organization, scopev1.Role_ROLE_OWNER)),
		member: newCaller(suite.URL, suite.PersonToken(t, suite.Organization, scopev1.Role_ROLE_MEMBER)),
		admin:  newCaller(suite.URL, suite.PersonToken(t, suite.Organization, scopev1.Role_ROLE_ADMIN)),
		other:  newCaller(suite.URL, suite.PersonToken(t, suite.OtherOrganization, scopev1.Role_ROLE_OWNER)),
	}
	t.Run("uploads", func(t *testing.T) { runUploads(t, r) })
	t.Run("jobs", func(t *testing.T) { runJobs(t, r) })
	t.Run("agent", func(t *testing.T) { runAgent(t, r) })
	t.Run("a call bearing no token is refused as unauthenticated", func(t *testing.T) { refusesACallBearingNoToken(t, r) })
}

func refuseIncompleteSuite(t *testing.T, suite Suite) {
	t.Helper()

	switch {
	case suite.URL == "":
		t.Fatal("the suite names no URL, so there is no runner to call")
	case suite.Runner == "":
		t.Fatal("the suite names no runner id, which every job token it mints must name")
	case suite.Organization == "" || suite.OtherOrganization == "" || suite.Organization == suite.OtherOrganization:
		t.Fatal("the suite needs two organizations, so it can prove one is kept from another's sources and jobs")
	case suite.PersonToken == nil || suite.JobToken == nil:
		t.Fatal("the suite needs to mint a person's token and a job token")
	}
}

type runnerUnderTest struct {
	suite  Suite
	mine   caller
	member caller
	admin  caller
	other  caller
}

type caller struct {
	token  string
	runner runnerv1connect.RunnerServiceClient
	agent  runnerv1connect.JobAgentServiceClient
}

func newCaller(url, token string) caller {
	url = strings.TrimRight(url, "/")
	bearer := connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			if token != "" {
				req.Header().Set("Authorization", "Bearer "+token)
			}
			return next(ctx, req)
		}
	}))
	return caller{
		token:  token,
		runner: runnerv1connect.NewRunnerServiceClient(client, url, bearer),
		agent:  runnerv1connect.NewJobAgentServiceClient(client, url, bearer),
	}
}

func send(t *testing.T, target *runnerv1.Upload, body string) int {
	t.Helper()

	payload, contentType := io.Reader(strings.NewReader(body)), ""
	if target.GetMethod() == http.MethodPost {
		payload, contentType = postPolicyForm(t, target, body)
	}
	req, err := http.NewRequestWithContext(t.Context(), target.GetMethod(), target.GetUrl(), payload)
	if err != nil {
		t.Fatal(err)
	}
	for name, value := range target.GetHeaders() {
		req.Header.Set(name, value)
	}
	if contentType != "" {
		req.Header.Set("Content-Type", contentType)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("%s the upload target for %s: %v", target.GetMethod(), target.GetDigest(), err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func postPolicyForm(t *testing.T, target *runnerv1.Upload, body string) (io.Reader, string) {
	t.Helper()

	var form bytes.Buffer
	writer := multipart.NewWriter(&form)
	for name, value := range target.GetFields() {
		if err := writer.WriteField(name, value); err != nil {
			t.Fatal(err)
		}
	}
	file, err := writer.CreateFormFile("file", target.GetDigest())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := io.WriteString(file, body); err != nil {
		t.Fatal(err)
	}
	if err := writer.Close(); err != nil {
		t.Fatal(err)
	}
	return &form, writer.FormDataContentType()
}

func fetch(t *testing.T, url string) []byte {
	t.Helper()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, url, nil)
	if err != nil {
		t.Fatal(err)
	}
	resp, err := client.Do(req)
	if err != nil {
		t.Fatalf("GET a blob's signed URL: %v", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	if resp.StatusCode != http.StatusOK {
		t.Fatalf("GET a blob's signed URL answered %d: %s", resp.StatusCode, body)
	}
	return body
}

func (c caller) target(t *testing.T, digest string) *runnerv1.Upload {
	t.Helper()

	targets := c.targets(t, digest)
	if len(targets) != 1 || targets[0].GetDigest() != digest {
		t.Fatalf("ListMissingBlobs(%s) handed out %v, want one upload target for it", digest, targets)
	}
	return targets[0]
}

func (c caller) upload(t *testing.T, path string) *runnerv1.SourceFile {
	t.Helper()

	file, _ := c.uploadContent(t, path)
	return file
}

func (c caller) uploadContent(t *testing.T, path string) (*runnerv1.SourceFile, string) {
	t.Helper()

	body := freshContent()
	digest := digestOf(body)
	if status := send(t, c.target(t, digest), body); status/100 != 2 {
		t.Fatalf("the upload target for %s answered %d, want the blob kept", digest, status)
	}
	return &runnerv1.SourceFile{Path: path, Digest: digest, Size: uint64(len(body))}, body
}

func (c caller) commit(t *testing.T, files ...*runnerv1.SourceFile) string {
	t.Helper()

	committed, err := c.runner.CommitSource(t.Context(), &runnerv1.CommitSourceRequest{Manifest: &runnerv1.SourceManifest{Files: files}})
	if err != nil {
		t.Fatalf("CommitSource() of uploaded files error = %v, want the source committed", err)
	}
	return committed.GetSourceId()
}

func (c caller) targets(t *testing.T, digests ...string) []*runnerv1.Upload {
	t.Helper()

	missing, err := c.runner.ListMissingBlobs(t.Context(), &runnerv1.ListMissingBlobsRequest{Digests: digests})
	if err != nil {
		t.Fatalf("ListMissingBlobs() error = %v", err)
	}
	if err := protovalidate.Validate(missing); err != nil {
		t.Fatalf("ListMissingBlobs() answered %v, which breaks the upload target rules: %v", missing, err)
	}
	return missing.GetUploads()
}

func (c caller) missing(t *testing.T, digests ...string) []string {
	t.Helper()

	var missing []string
	for _, target := range c.targets(t, digests...) {
		missing = append(missing, target.GetDigest())
	}
	return missing
}

func freshContent() string {
	return fmt.Sprintf("export const conformance = %q\n", rand.Text())
}

func digestOf(body string) string {
	sum := sha256.Sum256([]byte(body))
	return hex.EncodeToString(sum[:])
}

func newJobID() string {
	return "job_" + strings.ToLower(rand.Text())
}

const conformanceProject = "00000000-0000-4000-8000-000000000001"

func (r runnerUnderTest) jobToken(t *testing.T, organization, id string) string {
	t.Helper()

	return r.suite.JobToken(t, organization, r.jobTokenClaims(id))
}

func (r runnerUnderTest) jobTokenClaims(id string) endpoint.Job {
	return endpoint.Job{
		ID: id, ProjectID: conformanceProject, DeploymentID: strings.Repeat("ab", 16), Runner: r.suite.Runner,
		Slug: "conformance", Tier: environment.TierProduction, Expires: time.Now().Add(jobLife),
	}
}

func buildRequest(source string) *runnerv1.StartJobRequest {
	return commandRequest(runnerv1.JobCommand_JOB_COMMAND_DEPLOY, source)
}

func commandRequest(command runnerv1.JobCommand, source string) *runnerv1.StartJobRequest {
	return &runnerv1.StartJobRequest{Job: &runnerv1.JobRequest{
		Command: command,
		Source:  &runnerv1.JobRequest_SourceId{SourceId: source},
	}}
}

type startedJob struct {
	id     string
	job    *runnerv1.Job
	agent  caller
	source string
}

func (r runnerUnderTest) start(t *testing.T) startedJob {
	t.Helper()

	return r.startCommand(t, runnerv1.JobCommand_JOB_COMMAND_DEPLOY)
}

func (r runnerUnderTest) startCommand(t *testing.T, command runnerv1.JobCommand) startedJob {
	t.Helper()

	source := r.mine.commit(t, r.mine.upload(t, "index.ts"))
	id := newJobID()
	agent := newCaller(r.suite.URL, r.jobToken(t, r.suite.Organization, id))
	started, err := agent.runner.StartJob(t.Context(), commandRequest(command, source))
	if err != nil {
		t.Fatalf("StartJob() of a committed source error = %v, want a job", err)
	}
	r.mine.cancelWhenTheTestEnds(t, id)
	return startedJob{id: id, job: started.GetJob(), agent: agent, source: source}
}

func (r runnerUnderTest) startRunning(t *testing.T) startedJob {
	t.Helper()

	return r.startRunningCommand(t, runnerv1.JobCommand_JOB_COMMAND_DEPLOY)
}

func (r runnerUnderTest) startRunningCommand(t *testing.T, command runnerv1.JobCommand) startedJob {
	t.Helper()

	job := r.startCommand(t, command)
	if _, err := job.agent.agent.GetSpec(t.Context(), &runnerv1.GetSpecRequest{}); err != nil {
		t.Fatalf("GetSpec() as job %s's agent error = %v", job.id, err)
	}
	r.mine.await(t, job.id, runnerv1.JobState_JOB_STATE_RUNNING)
	return job
}

func (r runnerUnderTest) startSucceeded(t *testing.T) startedJob {
	t.Helper()

	job := r.startRunning(t)
	job.succeed(t, nil)
	r.mine.await(t, job.id, runnerv1.JobState_JOB_STATE_SUCCEEDED)
	return job
}

func (j startedJob) succeed(t *testing.T, urls []string) {
	t.Helper()

	summary := `{"summary":{"success":true,"headline":"Deployed conformance"`
	if len(urls) > 0 {
		summary += `,"apps":[{"urls":["` + strings.Join(urls, `","`) + `"]}]`
	}
	summary += `}}`
	j.appendOutput(t, 0, summary)
	if _, err := j.agent.agent.Finish(t.Context(), &runnerv1.FinishRequest{}); err != nil {
		t.Fatalf("Finish() as job %s's agent error = %v", j.id, err)
	}
}

func (j startedJob) appendOutput(t *testing.T, offset int64, lines ...string) *runnerv1.AppendOutputResponse {
	t.Helper()

	appended, err := j.agent.agent.AppendOutput(t.Context(), &runnerv1.AppendOutputRequest{Lines: lines, Offset: offset})
	if err != nil {
		t.Fatalf("AppendOutput() as job %s's agent error = %v", j.id, err)
	}
	return appended
}

func (c caller) cancelWhenTheTestEnds(t *testing.T, id string) {
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
		defer cancel()
		if _, err := c.runner.CancelJob(ctx, &runnerv1.CancelJobRequest{JobId: id}); err != nil {
			t.Errorf("CancelJob(%s) as the test ends error = %v, want the job ended so the next test finds the runner free", id, err)
		}
	})
}

func (c caller) await(t *testing.T, id string, want runnerv1.JobState) *runnerv1.Job {
	t.Helper()

	deadline := time.Now().Add(settleTimeout)
	for {
		read, err := c.runner.GetJob(t.Context(), &runnerv1.GetJobRequest{JobId: id})
		if err != nil {
			t.Fatalf("GetJob(%s) error = %v, want the job", id, err)
		}
		job := read.GetJob()
		if job.GetState() == want {
			return job
		}
		if isEnded(job.GetState()) {
			t.Fatalf("job %s ended %s, want it %s", id, job.GetState(), want)
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s is %s after %s, want it %s", id, job.GetState(), settleTimeout, want)
		}
		time.Sleep(20 * time.Millisecond)
	}
}

func isEnded(state runnerv1.JobState) bool {
	switch state {
	case runnerv1.JobState_JOB_STATE_SUCCEEDED, runnerv1.JobState_JOB_STATE_FAILED, runnerv1.JobState_JOB_STATE_CANCELED:
		return true
	}
	return false
}

func refusedWith(err error, want connect.Code) bool {
	return connect.CodeOf(err) == want
}

func refusesAsNotFound(t *testing.T, call string, err error) {
	t.Helper()

	if !refusedWith(err, connect.CodeNotFound) {
		t.Errorf("%s error = %v, want %s: an organization learns nothing of what another holds", call, err, connect.CodeNotFound)
	}
}

func refusesAsDenied(t *testing.T, call string, err error) {
	t.Helper()

	if !refusedWith(err, connect.CodePermissionDenied) {
		t.Errorf("%s error = %v, want %s", call, err, connect.CodePermissionDenied)
	}
}

func refusesACallBearingNoToken(t *testing.T, r runnerUnderTest) {
	anonymous := newCaller(r.suite.URL, "")

	_, err := anonymous.runner.ListMissingBlobs(t.Context(), &runnerv1.ListMissingBlobsRequest{Digests: []string{digestOf(freshContent())}})
	if !refusedWith(err, connect.CodeUnauthenticated) {
		t.Errorf("ListMissingBlobs() bearing no token error = %v, want %s", err, connect.CodeUnauthenticated)
	}
	_, err = anonymous.runner.GetJob(t.Context(), &runnerv1.GetJobRequest{JobId: "job_never_started"})
	if !refusedWith(err, connect.CodeUnauthenticated) {
		t.Errorf("GetJob() bearing no token error = %v, want %s", err, connect.CodeUnauthenticated)
	}
	_, err = anonymous.runner.StartJob(t.Context(), buildRequest(strings.Repeat("0", 64)))
	if !refusedWith(err, connect.CodeUnauthenticated) {
		t.Errorf("StartJob() bearing no token error = %v, want %s", err, connect.CodeUnauthenticated)
	}
	_, err = anonymous.agent.GetSpec(t.Context(), &runnerv1.GetSpecRequest{})
	if !refusedWith(err, connect.CodeUnauthenticated) {
		t.Errorf("GetSpec() bearing no token error = %v, want %s", err, connect.CodeUnauthenticated)
	}
}
