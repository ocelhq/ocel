package host

import (
	"context"
	"errors"
	"strings"
	"testing"

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

func readsLogs(command string) bool { return strings.Contains(command, "docker logs") }

func logging(env ssh.Result, logs string) func(*bench) {
	return func(box *bench) {
		answered := box.answer
		box.answer = func(command string) (ssh.Result, bool) {
			switch {
			case readsEnv(command):
				return env, true
			case readsLogs(command):
				return ssh.Result{Stdout: logs}, true
			}
			return answered(command)
		}
	}
}

func refusalsQuotingLogs() map[string]func(t *testing.T, arrange func(*bench)) (*bench, error) {
	return map[string]func(t *testing.T, arrange func(*bench)) (*bench, error){
		"a failed gate": func(t *testing.T, arrange func(*bench)) (*bench, error) {
			box := benched(t, ssh.Result{Code: 4, Stderr: nextTarget + " never answered /healthz within 30s"}, ssh.Result{})
			stated := box.answer
			box.answer = func(command string) (ssh.Result, bool) {
				if strings.HasPrefix(command, "docker inspect") && strings.Contains(command, ".State.") {
					return ssh.Result{Stdout: runningState}, true
				}
				return stated(command)
			}
			arrange(box.bench)
			return box.bench, box.host().containers.RollOut(context.Background(), aRollout(), progress.Discard())
		},
		"a health path search that never answered": func(t *testing.T, arrange func(*bench)) (*bench, error) {
			box := searchedOn(ssh.Result{Code: 4, Stderr: nextTarget + " never answered /up within 1m0s"})
			arrange(box)
			_, err := box.host().containers.FindHealthPath(context.Background(), aHealthPathSearch(), progress.Discard())
			return box, err
		},
		"a container that exited before it answered": func(t *testing.T, arrange func(*bench)) (*bench, error) {
			box := searchedOnCrashing(ssh.Result{Code: 1, Stdout: release.UnstartedMarker + " " + physical + "\n"}, crashLooping)
			arrange(box)
			_, err := box.host().containers.FindHealthPath(context.Background(), aHealthPathSearch(), progress.Discard())
			return box, err
		},
	}
}

func refusedWith(t *testing.T, what string, refused func(t *testing.T, arrange func(*bench)) (*bench, error), arrange func(*bench)) (*bench, string) {
	t.Helper()
	box, err := refused(t, arrange)
	if err == nil {
		t.Fatalf("%s returned no error at all", what)
	}
	return box, err.Error()
}

func TestARefusalQuotesTheAppsLogsWithEveryValueItsContainerWasHandedRedacted(t *testing.T) {
	t.Parallel()

	for what, refused := range refusalsQuotingLogs() {
		_, said := refusedWith(t, what, refused, logging(handedEnv, leakingLogs))
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

func TestARefusalRedactsAValueThatStartsInsideAnotherWholly(t *testing.T) {
	t.Parallel()

	env := ssh.Result{Stdout: `["API_BASE=https://api.shop.example","API_TOKEN=example/tok_9f8e7d6c5b4a"]` + "\n"}
	for what, refused := range refusalsQuotingLogs() {
		_, said := refusedWith(t, what, refused, logging(env, "2026-01-01T00:00:01Z calling https://api.shop.example/tok_9f8e7d6c5b4a failed\n"))
		if strings.Contains(said, "tok_9f8e7d6c5b4a") {
			t.Errorf("%s is refused with\n%s\nwhich quotes the part of API_TOKEN that API_BASE's match ended inside", what, said)
		}
		if !strings.Contains(said, "calling [redacted API_BASE, API_TOKEN] failed") {
			t.Errorf("%s is refused with\n%s\nand never names both values it redacted", what, said)
		}
	}
}

func TestARefusalWithholdsTheAppsLogsAndSaysWhyWhenItCannotReadTheValuesToRedact(t *testing.T) {
	t.Parallel()

	gone := ssh.Result{Code: 1, Stderr: "Error: No such object: " + physical}
	for what, refused := range refusalsQuotingLogs() {
		_, said := refusedWith(t, what, refused, logging(gone, leakingLogs))
		if strings.Contains(said, handedDatabaseURL) {
			t.Errorf("%s is refused with\n%s\nwhich quotes logs it could not redact", what, said)
		}
		if !strings.Contains(said, release.LogsWithheld+": Error: No such object: "+physical) {
			t.Errorf("%s is refused with\n%s\nand never says its logs were withheld, or why", what, said)
		}
	}
}

func TestARefusalWhoseLogsCouldNotBeReadSaysWhyAndReadsNoValues(t *testing.T) {
	t.Parallel()

	for what, refused := range refusalsQuotingLogs() {
		box, said := refusedWith(t, what, refused, func(box *bench) {
			logging(handedEnv, leakingLogs)(box)
			box.broke = func(command string) error {
				if readsLogs(command) {
					return errors.New("connection reset by peer")
				}
				return nil
			}
		})
		if !strings.Contains(said, "(could not be read: connection reset by peer)") {
			t.Errorf("%s is refused with\n%s\nand never says why its logs could not be read", what, said)
		}
		if strings.Contains(said, release.LogsWithheld) {
			t.Errorf("%s is refused with\n%s\nand blames redaction for logs it never read", what, said)
		}
		if box.at("{{json .Config.Env}}") >= 0 {
			t.Errorf("%s read its container's values to redact logs it never read: %v", what, box.commands())
		}
	}
}
