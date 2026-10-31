package runnerclient

import (
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/clierror"
	"github.com/ocelhq/ocel/cli/internal/version"
	endpointv1 "github.com/ocelhq/ocel/pkg/proto/endpoint/v1"
	runnerv1 "github.com/ocelhq/ocel/pkg/proto/runner/v1"
)

func versionUnserved(t *testing.T, served ...string) *connect.Error {
	t.Helper()
	refused := connect.NewError(connect.CodeFailedPrecondition, errors.New("this Ocel Cloud serves another release"))
	detail, err := connect.NewErrorDetail(&endpointv1.VersionUnserved{Served: served})
	if err != nil {
		t.Fatal(err)
	}
	refused.AddDetail(detail)
	return refused
}

func runningVersion(t *testing.T, v string) {
	t.Helper()
	running := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = running })
}

func TestACLIOlderThanEveryReleaseOcelCloudServesIsToldToUpgrade(t *testing.T) {
	runningVersion(t, "1.4.0")
	fake := &fakeJobs{refused: versionUnserved(t, "1.5.0", "1.6.0")}
	c, _ := newTestClient(serveJobs(t, fake), &staticTokens{})

	_, err := c.TailJob(t.Context(), "job_1", Tail{Changed: func(*runnerv1.Job) {}})
	got := clierror.NewRunError(err)
	if got.GetCode() != clierror.CodeJobVersionUnserved || !strings.Contains(got.GetMessage(), "upgrade ocel to 1.6.0") {
		t.Fatalf("run error = %v, want %s telling ocel 1.4.0 to upgrade", got, clierror.CodeJobVersionUnserved)
	}
}

func TestACLINewerThanTheReleaseOcelCloudServesIsToldWhichReleaseItServes(t *testing.T) {
	runningVersion(t, "1.4.0")
	fake := &fakeJobs{refused: versionUnserved(t, "1.3.0")}
	c, _ := newTestClient(serveJobs(t, fake), &staticTokens{})

	_, err := c.TailJob(t.Context(), "job_1", Tail{Changed: func(*runnerv1.Job) {}})
	got := clierror.NewRunError(err)
	if got.GetCode() != clierror.CodeJobVersionUnserved || !strings.Contains(got.GetMessage(), "serves ocel 1.3.0, not ocel 1.4.0") || strings.Contains(got.GetMessage(), "upgrade") {
		t.Fatalf("run error = %v, want %s naming the release Ocel Cloud serves, and no upgrade", got, clierror.CodeJobVersionUnserved)
	}
}

func TestAnUploadToAnOcelCloudThatDoesNotServeTheCLIStopsBeforeAnyBlobIsSent(t *testing.T) {
	fake := &fakeCloud{refused: versionUnserved(t, "9.9.9")}
	c, _ := newTestClient(serve(t, fake), &staticTokens{})

	_, _, _, err := upload(t, c, memorySource{"a.ts": "alpha"})
	if got := clierror.NewRunError(err); got.GetCode() != clierror.CodeJobVersionUnserved {
		t.Fatalf("run error = %v, want %s", got, clierror.CodeJobVersionUnserved)
	}
	if len(fake.stored) != 0 {
		t.Fatalf("stored %d blobs, want none", len(fake.stored))
	}
}

func TestACommitToAnOcelCloudThatDoesNotServeTheCLIFailsAsUnserved(t *testing.T) {
	fake := &fakeCloud{refused: versionUnserved(t, "9.9.9")}
	c, _ := newTestClient(serve(t, fake), &staticTokens{})

	_, err := c.Commit(t.Context(), memorySource{"a.ts": "alpha"}.manifest())
	if got := clierror.NewRunError(err); got.GetCode() != clierror.CodeJobVersionUnserved {
		t.Fatalf("run error = %v, want %s", got, clierror.CodeJobVersionUnserved)
	}
}

func TestATokenOcelCloudRefusesFailsAsRefusedAndSaysToLogInAgain(t *testing.T) {
	fake := &fakeJobs{reads: []jobRead{failure(connect.CodeUnauthenticated)}}
	c, _ := newTestClient(serveJobs(t, fake), &staticTokens{})

	_, err := c.TailJob(t.Context(), "job_1", Tail{Changed: func(*runnerv1.Job) {}})
	got := clierror.NewRunError(err)
	if got.GetCode() != clierror.CodeJobTokenRefused || got.GetHint() != "ocel login" {
		t.Fatalf("run error = %v, want %s hinting ocel login", got, clierror.CodeJobTokenRefused)
	}
}

func TestOcelCloudUnableToCheckATokenNeverSaysToLogInAgainOrEndsTheWait(t *testing.T) {
	fake := &fakeJobs{reads: []jobRead{
		job(runnerv1.JobState_JOB_STATE_RUNNING, 0, ""),
		failure(connect.CodeUnavailable),
		job(runnerv1.JobState_JOB_STATE_SUCCEEDED, 0, ""),
	}}
	c, _ := newTestClient(serveJobs(t, fake), &staticTokens{})

	got, err := c.TailJob(t.Context(), "job_1", Tail{Changed: func(*runnerv1.Job) {}})
	if err != nil || got.GetState() != runnerv1.JobState_JOB_STATE_SUCCEEDED {
		t.Fatalf("got %v, err = %v, want the wait to outlast Ocel Cloud being unable to check the token", got, err)
	}
}
