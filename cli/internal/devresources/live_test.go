package devresources_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/devresources"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	projectpkg "github.com/ocelhq/ocel/cli/internal/project"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
)

func TestDockerAWorkerWhoseLastTaskIsDeletedNoLongerRuns(t *testing.T) {
	if os.Getenv(liveEnv) == "" {
		t.Skipf("no docker daemon promised to this run; set %s=1 where one is running", liveEnv)
	}
	ctx := context.Background()
	const project = "devresources-live-forget-test"
	t.Cleanup(func() {
		engine, err := docker.Open(ctx)
		if err != nil {
			return
		}
		_ = engine.Wipe(ctx, docker.ProjectLabels(project))
		_ = engine.Close()
	})
	dir := t.TempDir()
	stack := devresources.New(project, devresources.Options{
		Open:     docker.Open,
		StateDir: t.TempDir(),
		Project:  &projectpkg.Project{Dir: dir, Slug: project, Apps: []projectpkg.App{{Name: "web", Path: "."}}},
	})
	t.Cleanup(func() { _ = stack.Close(ctx) })

	greet := declaration.Resource{Name: "greet", Type: resourcesv1.ResourceType_RESOURCE_TYPE_TASK, Task: &resourcesv1.TaskConfig{}, Source: filepath.Join(dir, "jobs", "index.ts") + ":3"}
	if _, err := stack.Resolve(ctx, []declaration.Resource{greet}); err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	if workers := stack.Workers(); len(workers) != 1 {
		t.Fatalf("workers = %+v, want the default worker serving greet", workers)
	}
	if _, err := stack.Resolve(ctx, nil); err != nil {
		t.Fatalf("Resolve with nothing declared = %v", err)
	}
	if workers := stack.Workers(); len(workers) != 0 {
		t.Fatalf("workers = %+v after the last task was deleted, want none to run", workers)
	}
}

const liveEnv = "OCEL_LIVE_DOCKER"

func TestDockerTwoProcessesOfOneProjectShareOnePostgres(t *testing.T) {
	if os.Getenv(liveEnv) == "" {
		t.Skipf("no docker daemon promised to this run; set %s=1 where one is running", liveEnv)
	}
	ctx := context.Background()
	const project = "devresources-live-shared-test"
	state := t.TempDir()
	engine, err := docker.Open(ctx)
	if err != nil {
		t.Fatalf("Open = %v", err)
	}
	t.Cleanup(func() {
		_ = engine.Wipe(ctx, docker.ProjectLabels(project))
		_ = engine.Close()
	})

	first := devresources.New(project, devresources.Options{Open: docker.Open, StateDir: state})
	second := devresources.New(project, devresources.Options{Open: docker.Open, StateDir: state})
	t.Cleanup(func() {
		_ = first.Close(ctx)
		_ = second.Close(ctx)
	})
	mine, err := first.Resolve(ctx, []declaration.Resource{postgres("main")})
	if err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	theirs, err := second.Resolve(ctx, []declaration.Resource{postgres("main")})
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
