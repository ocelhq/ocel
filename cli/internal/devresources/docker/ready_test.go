package docker_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker/dockertest"
)

func TestWaitReady(t *testing.T) {
	t.Parallel()

	t.Run("returns once the probe answers", func(t *testing.T) {
		t.Parallel()

		calls := 0
		err := docker.WaitReady(context.Background(), &dockertest.Engine{}, "db", time.Second, func(context.Context) error {
			calls++
			if calls < 3 {
				return errors.New("not yet")
			}
			return nil
		})
		if err != nil {
			t.Fatalf("WaitReady = %v, want nil", err)
		}
		if calls != 3 {
			t.Fatalf("probe ran %d times, want 3", calls)
		}
	})

	t.Run("gives up at the deadline and says what the probe last saw", func(t *testing.T) {
		t.Parallel()

		err := docker.WaitReady(context.Background(), &dockertest.Engine{}, "db", 50*time.Millisecond, func(context.Context) error {
			return errors.New("connection refused")
		})
		if err == nil || !strings.Contains(err.Error(), "connection refused") {
			t.Fatalf("WaitReady = %v, want the probe's last error", err)
		}
	})

	t.Run("stops at the first probe after the container exited and says how it exited", func(t *testing.T) {
		t.Parallel()

		engine := &dockertest.Engine{Exits: map[string]*docker.Exited{
			"db": {Container: "db", Code: 137, OOMKilled: true, Logs: "FATAL:  could not map anonymous shared memory\n"},
		}}
		started := time.Now()
		calls := 0
		err := docker.WaitReady(context.Background(), engine, "db", time.Minute, func(context.Context) error {
			calls++
			return errors.New("container db is not running")
		})
		if waited := time.Since(started); waited > 5*time.Second {
			t.Fatalf("WaitReady waited %s on a container that had already exited", waited)
		}
		if calls != 1 {
			t.Errorf("probe ran %d times, want 1: a container that exited never becomes ready", calls)
		}
		var exited *docker.Exited
		if !errors.As(err, &exited) || exited.Code != 137 {
			t.Fatalf("WaitReady = %v, want the container's exit", err)
		}
		for _, want := range []string{"db", "137", "out of memory", "could not map anonymous shared memory"} {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("WaitReady = %q, want it to mention %q", err, want)
			}
		}
	})
}

func TestUnreachableNamesTheDaemonAndTheWayOut(t *testing.T) {
	t.Parallel()

	err := &docker.Unreachable{Address: "unix:///var/run/docker.sock", Err: errors.New("dial: no such file")}
	for _, want := range []string{"unix:///var/run/docker.sock", "DOCKER_HOST", "start docker", "no such file"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Unreachable = %q, want it to mention %q", err.Error(), want)
		}
	}
}
