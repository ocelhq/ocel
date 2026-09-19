package docker_test

import (
	"context"
	"errors"
	"net"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
)

const liveEnv = "OCEL_LIVE_DOCKER"

const liveImage = "postgres:17.6@sha256:00bc86618629af00d2937fdc5a5d63db3ff8450acf52f0636ec813c7f4902929"

func requireDocker(t *testing.T) docker.Engine {
	t.Helper()
	if os.Getenv(liveEnv) == "" {
		t.Skipf("no docker daemon promised to this run; set %s=1 where one is running", liveEnv)
	}
	engine, err := docker.Open(context.Background())
	if err != nil {
		t.Fatalf("Open = %v", err)
	}
	t.Cleanup(func() { _ = engine.Close() })
	return engine
}

func TestLiveAContainerRunsOnALoopbackPortAndItsVolumeOutlivesIt(t *testing.T) {
	engine := requireDocker(t)
	ctx := context.Background()
	labels := map[string]string{"dev.ocel.project": "docker-live-test", "dev.ocel.component": "postgres"}
	spec := docker.Spec{
		Name:       "ocel-dev-docker-live-test",
		Image:      liveImage,
		Env:        []string{"POSTGRES_PASSWORD=live"},
		Port:       5432,
		Volume:     "ocel-dev-docker-live-test",
		VolumePath: "/var/lib/postgresql/data",
		Labels:     labels,
	}
	t.Cleanup(func() { _ = engine.Wipe(ctx, labels) })

	running, err := engine.Run(ctx, spec)
	if err != nil {
		t.Fatalf("Run = %v", err)
	}
	if !strings.HasPrefix(running.Addr, "127.0.0.1:") {
		t.Fatalf("Addr = %q, want a loopback address docker chose", running.Addr)
	}
	err = docker.WaitReady(ctx, time.Minute, func(ctx context.Context) error {
		_, err := engine.Exec(ctx, running.ID, "pg_isready", "-h", "127.0.0.1")
		return err
	})
	if err != nil {
		t.Fatalf("the container never became ready: %v", err)
	}
	conn, err := net.DialTimeout("tcp", running.Addr, time.Second)
	if err != nil {
		t.Fatalf("nothing answers on the published port %s: %v", running.Addr, err)
	}
	_ = conn.Close()

	if out, err := engine.Exec(ctx, running.ID, "echo", "hello"); err != nil || strings.TrimSpace(out) != "hello" {
		t.Fatalf("Exec(echo hello) = %q, %v", out, err)
	}
	if out, err := engine.ExecInput(ctx, running.ID, "read on stdin\n", "cat"); err != nil || strings.TrimSpace(out) != "read on stdin" {
		t.Fatalf("ExecInput(cat) = %q, %v, want what was written to its stdin", out, err)
	}
	var failed *docker.ExecFailed
	if _, err := engine.Exec(ctx, running.ID, "false"); !errors.As(err, &failed) || failed.Code != 1 {
		t.Fatalf("Exec(false) = %v, want an ExecFailed carrying exit code 1", err)
	}

	if _, err := engine.Exec(ctx, running.ID, "touch", "/var/lib/postgresql/data/kept"); err != nil {
		t.Fatalf("write into the volume: %v", err)
	}
	if err := engine.Stop(ctx, running.ID); err != nil {
		t.Fatalf("Stop = %v", err)
	}

	again, err := engine.Run(ctx, spec)
	if err != nil {
		t.Fatalf("Run after Stop = %v", err)
	}
	if _, err := engine.Exec(ctx, again.ID, "test", "-e", "/var/lib/postgresql/data/kept"); err != nil {
		t.Fatalf("the volume did not outlive its container: %v", err)
	}

	if err := engine.Wipe(ctx, labels); err != nil {
		t.Fatalf("Wipe = %v", err)
	}
	fresh, err := engine.Run(ctx, spec)
	if err != nil {
		t.Fatalf("Run after Wipe = %v", err)
	}
	if _, err := engine.Exec(ctx, fresh.ID, "test", "-e", "/var/lib/postgresql/data/kept"); err == nil {
		t.Fatal("the volume survived Wipe")
	}
}
