package agent_test

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/endpoint"
	"github.com/ocelhq/ocel/pkg/environment"
	scopev1 "github.com/ocelhq/ocel/pkg/proto/common/scope/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
	"github.com/ocelhq/ocel/pkg/proto/runner/v1/runnerv1connect"
	"github.com/ocelhq/ocel/pkg/runner"
	"github.com/ocelhq/ocel/pkg/runner/agent"
)

const (
	personToken = "person-token"
	jobToken    = "job-token"
	jobID       = "job_agent_test"
)

type idleLauncher struct {
	mu       sync.Mutex
	launched []runner.Launch
}

func (l *idleLauncher) Launch(_ context.Context, launch runner.Launch) (string, error) {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.launched = append(l.launched, launch)
	return launch.JobID, nil
}

func (l *idleLauncher) Cancel(context.Context, string) error { return nil }

func newRunner(t *testing.T) (string, *idleLauncher) {
	t.Helper()
	person, err := endpoint.NewUserCaller("https://console.example.com", "org-a", "user-1", scopev1.Role_ROLE_OWNER, nil)
	if err != nil {
		t.Fatal(err)
	}
	callers := map[string]endpoint.Caller{
		personToken: person,
		jobToken: endpoint.NewJobCaller("https://console.example.com", "org-a", endpoint.Job{
			ID: jobID, ProjectID: "00000000-0000-4000-8000-000000000001", Runner: "runner-test", Slug: "web",
			Tier: environment.TierProduction, DeploymentID: strings.Repeat("ab", 16), Expires: time.Now().Add(time.Hour),
		}),
	}
	mux := http.NewServeMux()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if caller, ok := callers[strings.TrimPrefix(r.Header.Get("Authorization"), "Bearer ")]; ok {
			r = r.WithContext(endpoint.WithCaller(r.Context(), caller))
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	launcher := &idleLauncher{}
	s, err := runner.New(runner.Config{
		ID: "runner-test", Dir: t.TempDir(), URL: server.URL, Launcher: launcher,
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(s.Close)
	for _, handler := range []func() (string, http.Handler){s.RunnerServiceHandler, s.JobAgentServiceHandler, s.BlobsHandler} {
		path, h := handler()
		mux.Handle(path, h)
	}
	return server.URL, launcher
}

func bearing(token string) connect.ClientOption {
	return connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", "Bearer "+token)
			return next(ctx, req)
		}
	}))
}

func upload(t *testing.T, client runnerv1connect.RunnerServiceClient, files map[string]string) string {
	t.Helper()
	manifest := &runnerv1.SourceManifest{}
	bodies := map[string]string{}
	var digests []string
	for path, body := range files {
		sum := sha256.Sum256([]byte(body))
		digest := hex.EncodeToString(sum[:])
		bodies[digest] = body
		digests = append(digests, digest)
		manifest.Files = append(manifest.Files, &runnerv1.SourceFile{Path: path, Digest: digest, Size: uint64(len(body)), Executable: strings.HasSuffix(path, ".sh")})
	}
	missing, err := client.ListMissingBlobs(t.Context(), &runnerv1.ListMissingBlobsRequest{Digests: digests})
	if err != nil {
		t.Fatal(err)
	}
	for _, target := range missing.GetUploads() {
		req, err := http.NewRequestWithContext(t.Context(), target.GetMethod(), target.GetUrl(), strings.NewReader(bodies[target.GetDigest()]))
		if err != nil {
			t.Fatal(err)
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = io.Copy(io.Discard, resp.Body)
		resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			t.Fatalf("PUT %s answered %d", target.GetDigest(), resp.StatusCode)
		}
	}
	committed, err := client.CommitSource(t.Context(), &runnerv1.CommitSourceRequest{Manifest: manifest})
	if err != nil {
		t.Fatal(err)
	}
	return committed.GetSourceId()
}

const fakeOcel = `#!/bin/sh
test "$1 $2 $3 $4" = "deploy --json --tag v1" || { echo "ran with $*" >&2; exit 3; }
test -f "$OCEL_JOB_CONTEXT" || { echo "no job context" >&2; exit 4; }
grep -q '"jobId":"job_agent_test"' "$OCEL_JOB_CONTEXT" || { echo "the job context names another job" >&2; exit 5; }
test -f ocel.json || { echo "not in the project" >&2; exit 6; }
test -z "$OCEL_JOB_TOKEN" || { echo "the run was handed the token's variable" >&2; exit 7; }
echo '{"waiting":{"input":{"id":"in_1","confirmation":{"question":"Deploy web?"}}}}'
read answer
case "$answer" in *'"confirmed":true'*) ;; *) echo "answered $answer" >&2; exit 8 ;; esac
echo '{"resumed":{"reason":"answered"}}'
echo '{"summary":{"success":true,"headline":"Deployed web","apps":[{"urls":["https://web.example.com"]}]}}'
`

func TestAnAgentRunsAJobOnARunnerFromItsUploadedSourceThroughAnAnswerToSuccess(t *testing.T) {
	url, launcher := newRunner(t)
	person := runnerv1connect.NewRunnerServiceClient(http.DefaultClient, url, bearing(personToken))
	source := upload(t, person, map[string]string{"web/ocel.json": `{"slug":"web"}`, "web/README.md": "hello\n"})
	jobs := runnerv1connect.NewRunnerServiceClient(http.DefaultClient, url, bearing(jobToken))
	if _, err := jobs.StartJob(t.Context(), &runnerv1.StartJobRequest{Job: &runnerv1.JobRequest{
		Command: runnerv1.JobCommand_JOB_COMMAND_DEPLOY, Source: &runnerv1.JobRequest_SourceId{SourceId: source}, Tag: "v1", ProjectPath: "web",
	}}); err != nil {
		t.Fatal(err)
	}
	if len(launcher.launched) != 1 || launcher.launched[0].Token != jobToken {
		t.Fatalf("launched %+v, want the agent handed the job token", launcher.launched)
	}

	ocel := filepath.Join(t.TempDir(), "ocel")
	if err := os.WriteFile(ocel, []byte(fakeOcel), 0o755); err != nil {
		t.Fatal(err)
	}
	ended := make(chan error, 1)
	go func() {
		ended <- agent.Run(t.Context(), agent.Config{
			Runner: url, Token: launcher.launched[0].Token, Ocel: ocel, Dir: t.TempDir(),
			Env: []string{"PATH=/usr/bin:/bin", runner.TokenEnvVar + "=" + jobToken}, ReportEvery: 20 * time.Millisecond,
		})
	}()

	waiting := await(t, person, func(job *runnerv1.Job) bool { return job.GetState() == runnerv1.JobState_JOB_STATE_INPUT_REQUIRED })
	if _, err := person.AnswerConfirmation(t.Context(), &runnerv1.AnswerConfirmationRequest{JobId: jobID, InputId: waiting.GetInput().GetId(), Confirmed: true}); err != nil {
		t.Fatal(err)
	}
	select {
	case err := <-ended:
		if err != nil {
			t.Fatalf("the agent: %v", err)
		}
	case <-time.After(20 * time.Second):
		t.Fatal("the agent never ended")
	}
	done := await(t, person, func(job *runnerv1.Job) bool { return job.GetState() != runnerv1.JobState_JOB_STATE_RUNNING })
	if done.GetState() != runnerv1.JobState_JOB_STATE_SUCCEEDED || !slices.Equal(done.GetResult().GetUrls(), []string{"https://web.example.com"}) {
		page, _ := person.ListJobEvents(t.Context(), &runnerv1.ListJobEventsRequest{JobId: jobID})
		t.Fatalf("the job ended %v, want it succeeded with the run's URL; its events: %v", done, page.GetEvents())
	}
}

func await(t *testing.T, client runnerv1connect.RunnerServiceClient, until func(*runnerv1.Job) bool) *runnerv1.Job {
	t.Helper()
	deadline := time.Now().Add(20 * time.Second)
	for {
		read, err := client.GetJob(t.Context(), &runnerv1.GetJobRequest{JobId: jobID})
		if err != nil {
			t.Fatal(err)
		}
		if until(read.GetJob()) {
			return read.GetJob()
		}
		if time.Now().After(deadline) {
			t.Fatalf("the job is still %v", read.GetJob())
		}
		time.Sleep(20 * time.Millisecond)
	}
}
