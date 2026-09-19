package devstack_test

import (
	"context"
	"os"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/devstack"
	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
)

const liveEnv = "OCEL_LIVE_DOCKER"

func TestDockerTwoProcessesOfOneProjectShareOnePostgres(t *testing.T) {
	if os.Getenv(liveEnv) == "" {
		t.Skipf("no docker daemon promised to this run; set %s=1 where one is running", liveEnv)
	}
	ctx := context.Background()
	const project = "devstack-live-shared-test"
	state := t.TempDir()
	engine, err := docker.Open(ctx)
	if err != nil {
		t.Fatalf("Open = %v", err)
	}
	t.Cleanup(func() {
		_ = engine.Wipe(ctx, docker.ProjectLabels(project))
		_ = engine.Close()
	})

	first := devstack.New(project, devstack.Env{Open: docker.Open, StateDir: state})
	second := devstack.New(project, devstack.Env{Open: docker.Open, StateDir: state})
	t.Cleanup(func() {
		_ = first.Close(ctx)
		_ = second.Close(ctx)
	})
	mine, err := first.Resolve(ctx, []declare.Resource{postgres("main")})
	if err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	theirs, err := second.Resolve(ctx, []declare.Resource{postgres("main")})
	if err != nil {
		t.Fatalf("Resolve while another process runs the same stack = %v", err)
	}
	if mine[0].Env["OCEL_RESOURCE_POSTGRES_main"] != theirs[0].Env["OCEL_RESOURCE_POSTGRES_main"] {
		t.Fatalf("the two processes were bound to different servers:\n%v\n%v", mine[0].Env, theirs[0].Env)
	}

	name := docker.Name(project, "postgres", "17")
	if err := first.Close(ctx); err != nil {
		t.Fatalf("Close = %v", err)
	}
	if _, err := engine.Exec(ctx, name, "pg_isready", "-h", "127.0.0.1"); err != nil {
		t.Fatalf("the first process out took the second's postgres with it: %v", err)
	}
	if err := second.Close(ctx); err != nil {
		t.Fatalf("second Close = %v", err)
	}
	if _, err := engine.Exec(ctx, name, "pg_isready", "-h", "127.0.0.1"); err == nil {
		t.Fatal("the last process out left the postgres running")
	}
}
