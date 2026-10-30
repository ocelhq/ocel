package runner_test

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/runner"
)

type dockerCall struct {
	env  []string
	args []string
}

func recordingDocker(calls *[]dockerCall, running map[string]bool) func(context.Context, []string, ...string) (string, error) {
	return func(_ context.Context, env []string, args ...string) (string, error) {
		*calls = append(*calls, dockerCall{env: env, args: args})
		switch {
		case args[0] == "container" && args[1] == "inspect":
			if running[args[len(args)-1]] {
				return "0123", nil
			}
			return "", errors.New("docker container: exit status 1: Error: No such container")
		case args[0] == "run":
			running[runRun(args)] = true
		case args[0] == "container" && args[1] == "rm":
			if !running[args[len(args)-1]] {
				return "", errors.New("docker container: exit status 1: Error: No such container")
			}
			delete(running, args[len(args)-1])
		}
		return "", nil
	}
}

func runRun(args []string) string {
	return args[slices.Index(args, "--name")+1]
}

func TestTheDockerLauncherHandsTheAgentItsTokenByNameAloneAndItsDeadline(t *testing.T) {
	var calls []dockerCall
	now := time.Date(2026, 1, 2, 3, 4, 5, 0, time.UTC)
	d := &runner.Docker{
		Image: "ocel-job:test", RunnerURL: "https://runner.example.com", Network: "host",
		DockerSocket: "/var/run/docker.sock", CPUs: "2", Memory: "4g",
		Mounts: []string{"type=volume,source=ocel-cache,target=/cache"}, Env: []string{"OCEL_PROVIDERS_DIR=/opt/ocel/providers"},
		Now: func() time.Time { return now }, Run: recordingDocker(&calls, map[string]bool{}),
	}
	handle, err := d.Launch(context.Background(), runner.Launch{JobID: "job_1", OrganizationID: "org-a", Token: "the-token", Deadline: now.Add(40 * time.Minute)})
	if err != nil {
		t.Fatal(err)
	}
	if handle != "ocel-job-job_1" {
		t.Errorf("handle %q, want the container's name", handle)
	}
	run := calls[len(calls)-1]
	if strings.Contains(strings.Join(run.args, " "), "the-token") {
		t.Fatalf("docker run was given the token in its arguments: %q", run.args)
	}
	if !slices.Contains(run.env, runner.TokenEnvVar+"=the-token") {
		t.Errorf("docker run's own environment %q holds no job token to pass on by name", run.env)
	}
	want := "run --detach --rm --name ocel-job-job_1 --label dev.ocel.runner.job=job_1 --env OCEL_JOB_TOKEN --network host " +
		"--mount type=bind,source=/var/run/docker.sock,target=/var/run/docker.sock --env DOCKER_HOST=unix:///var/run/docker.sock " +
		"--cpus 2 --memory 4g --mount type=volume,source=ocel-cache,target=/cache --env OCEL_PROVIDERS_DIR=/opt/ocel/providers " +
		"ocel-job:test ocel-job --runner https://runner.example.com --timeout 40m0s"
	if got := strings.Join(run.args, " "); got != want {
		t.Errorf("docker %s\nwant   %s", got, want)
	}
}

func TestLaunchingAJobAlreadyLaunchedStartsNothingAndCancelingItTwiceIsNoError(t *testing.T) {
	var calls []dockerCall
	d := &runner.Docker{Image: "ocel-job:test", RunnerURL: "http://127.0.0.1:1", Run: recordingDocker(&calls, map[string]bool{})}
	launch := runner.Launch{JobID: "job_1", Token: "t"}
	first, err := d.Launch(context.Background(), launch)
	if err != nil {
		t.Fatal(err)
	}
	second, err := d.Launch(context.Background(), launch)
	if err != nil || second != first {
		t.Fatalf("a second launch = %q, %v, want the first's handle %q", second, err, first)
	}
	runs := 0
	for _, call := range calls {
		if call.args[0] == "run" {
			runs++
		}
	}
	if runs != 1 {
		t.Errorf("docker run ran %d times for one job, want once", runs)
	}
	for range 2 {
		if err := d.Cancel(context.Background(), first); err != nil {
			t.Fatalf("Cancel() = %v, want no error for a container already gone", err)
		}
	}
}

func TestAJobIDNoContainerCanBeNamedAfterIsRefused(t *testing.T) {
	d := &runner.Docker{Image: "ocel-job:test", RunnerURL: "http://127.0.0.1:1", Run: func(context.Context, []string, ...string) (string, error) {
		t.Fatal("docker ran for a job id it cannot name a container after")
		return "", nil
	}}
	if _, err := d.Launch(context.Background(), runner.Launch{JobID: "../job", Token: "t"}); err == nil {
		t.Fatal("Launch() of job ../job succeeded")
	}
}
