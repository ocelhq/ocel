package host

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
)

const stalledDocker = `#!/bin/sh
case "$1 $2" in
"image inspect") exit 1 ;;
esac
case "$1" in
pull)
	pulls=$(cat "$PULLS" 2>/dev/null || echo 0)
	echo $((pulls + 1)) >"$PULLS"
	if [ "$pulls" -lt "${STALLED_PULLS:-1000}" ]; then exec sleep 600; fi
	exit 0
	;;
exec | run | stop | rm) exec sleep 600 ;;
esac
exit 0
`

type scriptRun struct {
	said    string
	failed  error
	elapsed time.Duration
	pulls   string
}

func runAgainstStalledDocker(t *testing.T, script string, env ...string) scriptRun {
	t.Helper()
	bin := t.TempDir()
	executable(t, filepath.Join(bin, "docker"), stalledDocker)
	pulls := filepath.Join(bin, "pulls")
	run := exec.Command("sh", "-c", script)
	run.Env = append(append(os.Environ(), "PATH="+bin+":"+os.Getenv("PATH"), "PULLS="+pulls), env...)
	run.WaitDelay = 5 * time.Second
	started := time.Now()
	out, err := run.CombinedOutput()
	counted, _ := os.ReadFile(pulls)
	return scriptRun{said: string(out), failed: err, elapsed: time.Since(started), pulls: strings.TrimSpace(string(counted))}
}

func TestAPullThatStallsIsCutOffAndTriedAgainRatherThanWaitedOnForever(t *testing.T) {
	t.Parallel()

	ran := runAgainstStalledDocker(t, imagePulled("rustfs/rustfs:1.0.0", 3, 1), "STALLED_PULLS=1")
	if ran.failed != nil {
		t.Fatalf("a pull that stalled once and then answered failed the step: %v\n%s", ran.failed, ran.said)
	}
	if ran.pulls != "2" {
		t.Errorf("the image was pulled %s times, want the stalled attempt cut off and a second one made", ran.pulls)
	}
	if ran.elapsed > time.Minute {
		t.Errorf("a pull bounded at 1s per attempt took %s", ran.elapsed)
	}
}

func TestAPullThatStallsOnEveryAttemptFailsNamingTheImageAndTheBound(t *testing.T) {
	t.Parallel()

	ran := runAgainstStalledDocker(t, imagePulled("rustfs/rustfs:1.0.0", 2, 1))
	if ran.failed == nil {
		t.Fatalf("a pull that never finished passed:\n%s", ran.said)
	}
	for _, want := range []string{"rustfs/rustfs:1.0.0", "2 attempts", "1s"} {
		if !strings.Contains(ran.said, want) {
			t.Errorf("the stalled pull said %q, want it to name %q", strings.TrimSpace(ran.said), want)
		}
	}
}

func TestADockerStepThatStallsFailsNamingWhatStalledAndHowLongItWaited(t *testing.T) {
	t.Parallel()

	ran := runAgainstStalledDocker(t, boundedStep(1, "docker run of shop-store", "docker run --detach shop-store >/dev/null"))
	if ran.failed == nil {
		t.Fatalf("a docker run that never returned passed:\n%s", ran.said)
	}
	if want := "docker run of shop-store did not finish within 1s"; !strings.Contains(ran.said, want) {
		t.Errorf("the stalled step said %q, want %q", strings.TrimSpace(ran.said), want)
	}
	if ran.elapsed > 30*time.Second {
		t.Errorf("a step bounded at 1s took %s", ran.elapsed)
	}
}

func TestADockerStepThatFailsOnItsOwnKeepsItsStatusAndIsNotCalledAStall(t *testing.T) {
	t.Parallel()

	ran := runAgainstStalledDocker(t, boundedStep(1, "docker volume create", "sh -c 'echo refused >&2; exit 3'")+"\necho unreached")
	var exit *exec.ExitError
	if ran.failed == nil || !errors.As(ran.failed, &exit) || exit.ExitCode() != 3 {
		t.Fatalf("a step that exited 3 left the script with %v:\n%s", ran.failed, ran.said)
	}
	if strings.Contains(ran.said, "did not finish") {
		t.Errorf("a step that failed at once was reported as a stall: %q", ran.said)
	}
}

func TestATakeDownToleratesAContainerAlreadyGoneButNotADockerThatNeverAnswers(t *testing.T) {
	t.Parallel()

	gone := runAgainstStalledDocker(t, boundedTolerated(1, "docker rm of shop-web", "sh -c 'exit 1'")+"\necho kept-going")
	if gone.failed != nil || !strings.Contains(gone.said, "kept-going") {
		t.Errorf("removing a container that is already gone stopped the script: %v\n%s", gone.failed, gone.said)
	}
	stuck := runAgainstStalledDocker(t, boundedTolerated(1, "docker rm of shop-web", "docker rm --force shop-web")+"\necho kept-going")
	if stuck.failed == nil || strings.Contains(stuck.said, "kept-going") {
		t.Fatalf("a docker rm that never returned was passed over: %v\n%s", stuck.failed, stuck.said)
	}
	if want := "docker rm of shop-web did not finish within 1s"; !strings.Contains(stuck.said, want) {
		t.Errorf("the stalled removal said %q, want %q", strings.TrimSpace(stuck.said), want)
	}
}

func TestAResourceWhoseProbeNeverReturnsIsGivenUpOnWithinItsWindow(t *testing.T) {
	t.Parallel()

	spec := resourced()
	spec.Ready = []string{"curl", "-fsS", "http://127.0.0.1:9000/health/ready"}
	ran := runAgainstStalledDocker(t, readyCommand(spec, 2))
	if ran.failed == nil {
		t.Fatalf("a resource whose probe never returned was called ready:\n%s", ran.said)
	}
	if want := spec.Name + " never answered"; !strings.Contains(ran.said, want) {
		t.Errorf("the readiness wait said %q, want it to name %q", strings.TrimSpace(ran.said), want)
	}
	if ran.elapsed > 45*time.Second {
		t.Errorf("a readiness window of 2s took %s, so a probe that hangs holds the deploy past its window", ran.elapsed)
	}
}

func TestEveryDockerCallThatTakesAContainerDownIsBounded(t *testing.T) {
	t.Parallel()

	for what, take := range map[string]func(*Host) error{
		"TakeDown": func(h *Host) error {
			return h.TakeDown(context.Background(), environment.TierProduction, physical)
		},
		"StopContainer":   func(h *Host) error { return h.StopContainer(context.Background(), physical) },
		"RemoveContainer": func(h *Host) error { return h.RemoveContainer(context.Background(), physical) },
		"RemoveResource": func(h *Host) error {
			return h.RemoveResource(context.Background(), ResourceRef{
				Tier: environment.TierProduction, Project: "shop", Resource: "store", Name: "shop-prod-store-s3",
			})
		},
	} {
		box := machine(nil)
		if err := take(box.host()); err != nil {
			t.Fatalf("%s() = %v", what, err)
		}
		for _, command := range box.commands() {
			for _, line := range strings.Split(command, "\n") {
				for _, call := range []string{"docker stop ", "docker rm ", "docker volume rm"} {
					if at := strings.Index(line, call); at >= 0 && !strings.Contains(line[:at], "timeout ") {
						t.Errorf("%s runs %q with no time bound, so a docker that stops answering hangs the destroy:\n%s", what, call, line)
					}
				}
			}
		}
	}
}

func TestAResourceIsRunWithinABound(t *testing.T) {
	t.Parallel()

	script := runResourceScript(resourced(), "0123456789ab", EnvFile(resourced().Tier, resourced().Name))
	for _, line := range strings.Split(script, "\n") {
		if at := strings.Index(line, quoted("docker")+" "+quoted("run")); at >= 0 && !strings.Contains(line[:at], "timeout ") {
			t.Errorf("the resource is started with no time bound on docker run:\n%s", line)
		}
	}
}

func TestAWaitOnTheRoutingLockGivesUpNamingTheLockRatherThanWaitingForever(t *testing.T) {
	t.Parallel()

	held := t.TempDir()
	holder := exec.Command("flock", "-x", held, "sleep", "30")
	if err := holder.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = holder.Process.Kill(); _ = holder.Wait() })
	deadline := time.Now().Add(10 * time.Second)
	for exec.Command("flock", "-n", "-x", held, "true").Run() == nil {
		if time.Now().After(deadline) {
			t.Fatal("the lock was never taken by the process meant to hold it")
		}
		time.Sleep(50 * time.Millisecond)
	}

	ran := runAgainstStalledDocker(t, lockedWithin(held, "-x", 1)+"echo got-the-lock")
	if ran.failed == nil || strings.Contains(ran.said, "got-the-lock") {
		t.Fatalf("a wait on a lock held elsewhere passed: %v\n%s", ran.failed, ran.said)
	}
	for _, want := range []string{held, "1s"} {
		if !strings.Contains(ran.said, want) {
			t.Errorf("the wait on the lock said %q, want it to name %q", strings.TrimSpace(ran.said), want)
		}
	}
	if ran.elapsed > 15*time.Second {
		t.Errorf("a wait bounded at 1s took %s", ran.elapsed)
	}
}

func TestASwitchboardStartedAgainPullsItsImageBeforeItTakesTheRoutingLock(t *testing.T) {
	t.Parallel()

	script := switchboardBox(nil, Front{}).restoring(containerRising)
	pull, locked := strings.Index(script, "docker pull"), strings.Index(script, routingLocked("-x"))
	if pull < 0 || locked < 0 {
		t.Fatalf("restoring the switchboard neither pulls nor locks:\n%s", script)
	}
	if pull > locked {
		t.Errorf("the switchboard's image is pulled while the routing lock is held, so a slow registry stops every other deploy on the box from routing:\n%s", script)
	}
}
