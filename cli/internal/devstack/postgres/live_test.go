package postgres_test

import (
	"context"
	"net"
	"os"
	"strconv"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
	"github.com/ocelhq/ocel/cli/internal/devstack/postgres"
)

const liveEnv = "OCEL_LIVE_DOCKER"

func TestLiveDeclaredDatabasesComeUpAndKeepTheirDataAcrossRuns(t *testing.T) {
	if os.Getenv(liveEnv) == "" {
		t.Skipf("no docker daemon promised to this run; set %s=1 where one is running", liveEnv)
	}
	ctx := context.Background()
	const project = "postgres-live-test"
	engine, err := docker.Open(ctx)
	if err != nil {
		t.Fatalf("Open = %v", err)
	}
	t.Cleanup(func() {
		_ = engine.RemoveVolumes(ctx, docker.ProjectLabels(project))
		_ = engine.Close()
	})

	first := postgres.New(docker.Open)
	resolved, err := first.Resolve(ctx, project, []declare.Resource{declared("main", "17"), declared("audit log", "17")})
	if err != nil {
		t.Fatalf("Resolve = %v", err)
	}
	props := binding(t, resolved[0].Env["OCEL_RESOURCE_POSTGRES_main"]).GetPostgres()
	conn, err := net.DialTimeout("tcp", net.JoinHostPort(props.GetHost(), strconv.Itoa(int(props.GetPort()))), time.Second)
	if err != nil {
		t.Fatalf("nothing answers where the binding points: %v", err)
	}
	_ = conn.Close()

	name := docker.Name(project, "postgres", "17")
	if _, err := engine.Exec(ctx, name, "psql", "-U", "postgres", "-d", "audit log", "-v", "ON_ERROR_STOP=1", "-c", "CREATE TABLE kept (id int)"); err != nil {
		t.Fatalf("the declared database is not there to write to: %v", err)
	}
	if err := first.Close(ctx); err != nil {
		t.Fatalf("Close = %v", err)
	}

	second := postgres.New(docker.Open)
	t.Cleanup(func() { _ = second.Close(ctx) })
	if _, err := second.Resolve(ctx, project, []declare.Resource{declared("audit log", "17")}); err != nil {
		t.Fatalf("Resolve on a second run = %v", err)
	}
	if _, err := engine.Exec(ctx, name, "psql", "-U", "postgres", "-d", "audit log", "-v", "ON_ERROR_STOP=1", "-c", "SELECT id FROM kept"); err != nil {
		t.Fatalf("the data did not survive the container being stopped: %v", err)
	}
}
