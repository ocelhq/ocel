package cloudconformance

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"buf.build/go/protovalidate"
	"connectrpc.com/connect"

	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	"github.com/ocelhq/ocel/pkg/proto/cloud/v1/cloudv1connect"
)

type Suite struct {
	URL                    string
	Token                  string
	OtherOrganizationToken string
	Version                string
	Builder                Builder
}

type Builder interface {
	Log(ctx context.Context, jobID string, lines []string) error
	Succeed(ctx context.Context, jobID string) error
}

const (
	versionHeader   = "Ocel-Version"
	unservedVersion = "0.0.0-unserved"
	settleTimeout   = 30 * time.Second
	settlePoll      = 20 * time.Millisecond
)

func Run(t *testing.T, suite Suite) {
	t.Helper()

	refuseIncompleteSuite(t, suite)
	c := cloud{
		suite:        suite,
		cli:          newCaller(suite.URL, suite.Token, suite.Version),
		otherCLI:     newCaller(suite.URL, suite.OtherOrganizationToken, suite.Version),
		console:      newCaller(suite.URL, suite.Token, ""),
		otherConsole: newCaller(suite.URL, suite.OtherOrganizationToken, ""),
	}
	t.Run("uploads", func(t *testing.T) { runUploads(t, c) })
	t.Run("jobs", func(t *testing.T) { runJobs(t, c) })
	t.Run("versions", func(t *testing.T) { runVersions(t, c) })
	t.Run("a call bearing no token is refused as unauthenticated", func(t *testing.T) { refusesACallBearingNoToken(t, c) })
}

func refuseIncompleteSuite(t *testing.T, suite Suite) {
	t.Helper()

	if suite.URL == "" {
		t.Fatal("the suite names no URL, so there is no Ocel Cloud to call")
	}
	if suite.Token == "" || suite.OtherOrganizationToken == "" || suite.Token == suite.OtherOrganizationToken {
		t.Fatal("the suite needs two tokens of two organizations, each holding cloud.apps.read and cloud.apps.write, " +
			"so nothing here proves one organization is kept from another's sources and jobs")
	}
	if suite.Version == "" {
		t.Fatal("the suite names no version, so it cannot call as a CLI the cloud serves")
	}
	if suite.Builder == nil {
		t.Fatal("the suite has no Builder, so no job it starts logs a line or ends")
	}
}

type cloud struct {
	suite        Suite
	cli          caller
	otherCLI     caller
	console      caller
	otherConsole caller
}

type caller struct {
	url     string
	token   string
	version string
	uploads cloudv1connect.UploadServiceClient
	jobs    cloudv1connect.JobServiceClient
}

func newCaller(url, token, version string) caller {
	url = strings.TrimRight(url, "/")
	headers := connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			setHeaders(req.Header(), token, version)
			return next(ctx, req)
		}
	}))
	return caller{
		url:     url,
		token:   token,
		version: version,
		uploads: cloudv1connect.NewUploadServiceClient(http.DefaultClient, url, headers),
		jobs:    cloudv1connect.NewJobServiceClient(http.DefaultClient, url, headers),
	}
}

func setHeaders(header http.Header, token, version string) {
	if token != "" {
		header.Set("Authorization", "Bearer "+token)
	}
	if version != "" {
		header.Set(versionHeader, version)
	}
}

func send(t *testing.T, target *cloudv1.Upload, body string) int {
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
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s the upload target for %s: %v", target.GetMethod(), target.GetDigest(), err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	_ = resp.Body.Close()
	return resp.StatusCode
}

func postPolicyForm(t *testing.T, target *cloudv1.Upload, body string) (io.Reader, string) {
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

func (c caller) target(t *testing.T, digest string) *cloudv1.Upload {
	t.Helper()

	targets := c.targets(t, digest)
	if len(targets) != 1 || targets[0].GetDigest() != digest {
		t.Fatalf("Missing(%s) handed out %v, want one upload target for it", digest, targets)
	}
	return targets[0]
}

func (c caller) upload(t *testing.T, path string) *cloudv1.SourceFile {
	t.Helper()

	body := freshContent()
	digest := digestOf(body)
	if status := send(t, c.target(t, digest), body); status/100 != 2 {
		t.Fatalf("the upload target for %s answered %d, want the blob kept", digest, status)
	}
	return &cloudv1.SourceFile{Path: path, Digest: digest, Size: uint64(len(body))}
}

func (c caller) commit(t *testing.T, files ...*cloudv1.SourceFile) string {
	t.Helper()

	committed, err := c.uploads.CommitSource(t.Context(), &cloudv1.CommitSourceRequest{Manifest: &cloudv1.SourceManifest{Files: files}})
	if err != nil {
		t.Fatalf("CommitSource() of uploaded files error = %v, want the source committed", err)
	}
	return committed.GetSourceId()
}

func (c caller) targets(t *testing.T, digests ...string) []*cloudv1.Upload {
	t.Helper()

	missing, err := c.uploads.Missing(t.Context(), &cloudv1.MissingRequest{Digests: digests})
	if err != nil {
		t.Fatalf("Missing() error = %v", err)
	}
	if err := protovalidate.Validate(missing); err != nil {
		t.Fatalf("Missing() answered %v, which breaks the upload target rules: %v", missing, err)
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

func buildRequest(source string) *cloudv1.StartJobRequest {
	return &cloudv1.StartJobRequest{ProjectId: "conformance-project", Slug: "conformance", Command: cloudv1.JobCommand_JOB_COMMAND_BUILD, SourceId: source}
}

func (c cloud) start(t *testing.T) *cloudv1.Job {
	t.Helper()

	source := c.cli.commit(t, c.cli.upload(t, "index.ts"))
	started, err := c.cli.jobs.StartJob(t.Context(), buildRequest(source))
	if err != nil {
		t.Fatalf("StartJob() of a committed source error = %v, want a job", err)
	}
	c.console.cancelWhenTheTestEnds(t, started.GetJob().GetId())
	return started.GetJob()
}

func (c cloud) startRunning(t *testing.T) *cloudv1.Job {
	t.Helper()

	return c.await(t, c.start(t).GetId(), cloudv1.JobState_JOB_STATE_RUNNING)
}

func (c cloud) startSucceeded(t *testing.T) *cloudv1.Job {
	t.Helper()

	job := c.startRunning(t)
	if err := c.suite.Builder.Succeed(t.Context(), job.GetId()); err != nil {
		t.Fatalf("Builder.Succeed(%s) error = %v", job.GetId(), err)
	}
	return c.await(t, job.GetId(), cloudv1.JobState_JOB_STATE_SUCCEEDED)
}

func (c caller) cancelWhenTheTestEnds(t *testing.T, id string) {
	if id == "" {
		return
	}
	t.Cleanup(func() {
		ctx, cancel := context.WithTimeout(context.Background(), settleTimeout)
		defer cancel()
		if _, err := c.jobs.CancelJob(ctx, &cloudv1.CancelJobRequest{JobId: id}); err != nil {
			t.Errorf("CancelJob(%s) as the test ends error = %v, want the job ended so the next test finds a builder free", id, err)
		}
	})
}

func (c cloud) await(t *testing.T, id string, want cloudv1.JobState) *cloudv1.Job {
	t.Helper()

	deadline := time.Now().Add(settleTimeout)
	for {
		read, err := c.console.jobs.GetJob(t.Context(), &cloudv1.GetJobRequest{JobId: id})
		if err != nil {
			t.Fatalf("GetJob(%s) error = %v, want the job", id, err)
		}
		job := read.GetJob()
		if job.GetState() == want {
			return job
		}
		if ended(job.GetState()) {
			t.Fatalf("job %s ended %s, want it %s", id, job.GetState(), want)
		}
		if time.Now().After(deadline) {
			t.Fatalf("job %s is %s after %s, want it %s", id, job.GetState(), settleTimeout, want)
		}
		time.Sleep(settlePoll)
	}
}

func ended(state cloudv1.JobState) bool {
	switch state {
	case cloudv1.JobState_JOB_STATE_SUCCEEDED, cloudv1.JobState_JOB_STATE_FAILED, cloudv1.JobState_JOB_STATE_CANCELED:
		return true
	}
	return false
}

func refusedWith(err error, want connect.Code) bool {
	return connect.CodeOf(err) == want
}

func versionUnserved(err error) *cloudv1.VersionUnserved {
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) {
		return nil
	}
	for _, detail := range connectErr.Details() {
		value, err := detail.Value()
		if unserved, ok := value.(*cloudv1.VersionUnserved); err == nil && ok {
			return unserved
		}
	}
	return nil
}

func refusesTheVersion(t *testing.T, call string, err error, served string) {
	t.Helper()

	if !refusedWith(err, connect.CodeFailedPrecondition) {
		t.Errorf("%s error = %v, want %s", call, err, connect.CodeFailedPrecondition)
		return
	}
	unserved := versionUnserved(err)
	if unserved == nil {
		t.Errorf("%s error = %v carries no cloud.v1.VersionUnserved detail, which is how a CLI learns which versions to install", call, err)
		return
	}
	if !slices.Contains(unserved.GetServed(), served) || slices.Contains(unserved.GetServed(), unservedVersion) {
		t.Errorf("%s VersionUnserved.served = %v, want it to name %s, which the cloud serves, and not %s", call, unserved.GetServed(), served, unservedVersion)
	}
}

func refusesAsNotFound(t *testing.T, call string, err error) {
	t.Helper()

	if !refusedWith(err, connect.CodeNotFound) {
		t.Errorf("%s error = %v, want %s: an organization learns nothing of what another holds", call, err, connect.CodeNotFound)
	}
}
