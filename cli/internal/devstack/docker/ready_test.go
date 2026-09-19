package docker_test

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
)

func TestWaitReady(t *testing.T) {
	t.Parallel()

	t.Run("returns once the probe answers", func(t *testing.T) {
		t.Parallel()

		calls := 0
		err := docker.WaitReady(context.Background(), time.Second, func(context.Context) error {
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

		err := docker.WaitReady(context.Background(), 50*time.Millisecond, func(context.Context) error {
			return errors.New("connection refused")
		})
		if err == nil || !strings.Contains(err.Error(), "connection refused") {
			t.Fatalf("WaitReady = %v, want the probe's last error", err)
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
