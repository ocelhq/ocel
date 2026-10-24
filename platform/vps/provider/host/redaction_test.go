package host

import (
	"context"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/platform/vps/provider/release"
	"github.com/ocelhq/ocel/platform/vps/provider/ssh"
)

const (
	handedDatabaseURL = "postgres://shop:hunter2-s3cret@db.internal:5432/shop"
	runningState      = "Status=running ExitCode=0 OOMKilled=false Error= StartedAt=2026-01-01T00:00:00Z FinishedAt=0001-01-01T00:00:00Z RestartCount=0"
	leakingLogs       = "2026-01-01T00:00:01Z listening on 3000\n" +
		"2026-01-01T00:00:02Z connect " + handedDatabaseURL + ": connection refused\n"
)

var handedEnv = ssh.Result{Stdout: `["PATH=/usr/local/bin:/usr/bin","DATABASE_URL=` + handedDatabaseURL + `","PORT=3000"]` + "\n"}

func readsEnv(command string) bool { return strings.Contains(command, "{{json .Config.Env}}") }

func loggingThrough(box *bench, env ssh.Result) {
	answered := box.answer
	box.answer = func(command string) (ssh.Result, bool) {
		switch {
		case readsEnv(command):
			return env, true
		case strings.Contains(command, "docker logs"):
			return ssh.Result{Stdout: leakingLogs}, true
		}
		return answered(command)
	}
}

func refusalsQuotingLogs() map[string]func(t *testing.T, env ssh.Result) error {
	return map[string]func(t *testing.T, env ssh.Result) error{
		"a failed gate": func(t *testing.T, env ssh.Result) error {
			box := benched(t, ssh.Result{Code: 4, Stderr: nextTarget + " never answered /healthz within 30s"}, ssh.Result{})
			loggingThrough(box.bench, env)
			stated := box.answer
			box.answer = func(command string) (ssh.Result, bool) {
				if strings.HasPrefix(command, "docker inspect") && strings.Contains(command, ".State.") {
					return ssh.Result{Stdout: runningState}, true
				}
				return stated(command)
			}
			return box.host().containers.RollOut(context.Background(), aRollout(), progress.Discard())
		},
		"a health path search that never answered": func(t *testing.T, env ssh.Result) error {
			box := searchedOn(ssh.Result{Code: 4, Stderr: nextTarget + " never answered /up within 1m0s"})
			loggingThrough(box, env)
			_, err := box.host().containers.FindHealthPath(context.Background(), aHealthPathSearch(), progress.Discard())
			return err
		},
		"a container that exited before it answered": func(t *testing.T, env ssh.Result) error {
			box := searchedOnCrashing(ssh.Result{Code: 1, Stdout: release.UnstartedMarker + " " + physical + "\n"}, crashLooping)
			loggingThrough(box, env)
			_, err := box.host().containers.FindHealthPath(context.Background(), aHealthPathSearch(), progress.Discard())
			return err
		},
	}
}

func TestARefusalQuotesTheAppsLogsWithEveryValueItsContainerWasHandedRedacted(t *testing.T) {
	t.Parallel()

	for what, refused := range refusalsQuotingLogs() {
		err := refused(t, handedEnv)
		if err == nil {
			t.Fatalf("%s returned no error at all", what)
		}
		said := err.Error()
		if strings.Contains(said, handedDatabaseURL) || strings.Contains(said, "hunter2-s3cret") {
			t.Errorf("%s is refused with\n%s\nwhich quotes the DATABASE_URL the container was handed", what, said)
		}
		if !strings.Contains(said, "connect [redacted DATABASE_URL]: connection refused") {
			t.Errorf("%s is refused with\n%s\nand never shows where the logs named DATABASE_URL", what, said)
		}
		if !strings.Contains(said, "listening on 3000") {
			t.Errorf("%s is refused with\n%s\nand redacted PORT's 3000, a value too short to be a secret", what, said)
		}
	}
}

func TestARefusalWithholdsTheAppsLogsWhenItCannotReadTheValuesToRedact(t *testing.T) {
	t.Parallel()

	for what, refused := range refusalsQuotingLogs() {
		err := refused(t, ssh.Result{Code: 1, Stderr: "Error response from daemon: No such container: " + physical + ":" + containerimage.PortText})
		if err == nil {
			t.Fatalf("%s returned no error at all", what)
		}
		said := err.Error()
		if strings.Contains(said, handedDatabaseURL) {
			t.Errorf("%s is refused with\n%s\nwhich quotes logs it could not redact", what, said)
		}
		if !strings.Contains(said, release.LogsWithheld) {
			t.Errorf("%s is refused with\n%s\nand never says its logs were withheld", what, said)
		}
	}
}
