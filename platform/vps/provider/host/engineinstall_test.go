package host

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func installer(t *testing.T, failures int) (string, string, string) {
	t.Helper()
	return installerSaying(t, failures, "")
}

func installerSaying(t *testing.T, failures int, said string) (string, string, string) {
	t.Helper()
	dir := t.TempDir()
	attempts := filepath.Join(dir, "attempts")
	fetched := filepath.Join(dir, "fetched")
	paused := filepath.Join(dir, "paused")

	executable(t, fetched, "#!/bin/sh\nprintf a >>"+quoted(attempts)+"\n"+said+"\n"+
		"[ \"$(wc -c <"+quoted(attempts)+")\" -gt "+strconv.Itoa(failures)+" ] || exit 1\n")
	executable(t, filepath.Join(dir, "curl"), `#!/bin/sh
named=0
for arg; do
if [ "$named" = 1 ]; then cp `+quoted(fetched)+` "$arg"; exit 0; fi
if [ "$arg" = -o ]; then named=1; fi
done
exit 1
`)
	executable(t, filepath.Join(dir, "sha256sum"), "#!/bin/sh\ncat >/dev/null\nexit 0\n")
	executable(t, filepath.Join(dir, "sleep"), "#!/bin/sh\nprintf '%s\\n' \"$1\" >>"+quoted(paused)+"\nexit 0\n")
	return dir, attempts, paused
}

func waited(t *testing.T, paused string) []int {
	t.Helper()
	read, err := os.ReadFile(paused)
	if err != nil {
		t.Fatal(err)
	}
	var seconds []int
	for _, line := range strings.Fields(string(read)) {
		held, err := strconv.Atoi(line)
		if err != nil {
			t.Fatalf("the engine step slept for %q, which is not a number of seconds", line)
		}
		seconds = append(seconds, held)
	}
	return seconds
}

func installing(t *testing.T, dir string) (string, error) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", engineCommand())
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	var said strings.Builder
	cmd.Stdout, cmd.Stderr = &said, &said
	err := cmd.Run()
	return said.String(), err
}

func TestAnInstallScriptThatFailedOnceIsRunAgainRatherThanFailingTheApply(t *testing.T) {
	t.Parallel()

	dir, attempts, _ := installer(t, 2)
	if said, err := installing(t, dir); err != nil {
		t.Fatalf("the engine step = %v on a host whose install script failed twice and then stood:\n%s", err, said)
	}
	ran, err := os.ReadFile(attempts)
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != 3 {
		t.Errorf("the install script ran %d times, want the two transient failures retried and the third kept", len(ran))
	}
}

func TestAnInstallScriptThatNeverStandsIsRefusedWithABound(t *testing.T) {
	t.Parallel()

	dir, attempts, _ := installer(t, 99)
	said, err := installing(t, dir)
	if err == nil {
		t.Fatalf("the engine step succeeded on a host whose install script never stood:\n%s", said)
	}
	ran, err := os.ReadFile(attempts)
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != engineInstallTries {
		t.Errorf("the install script ran %d times, want it bounded at %d: a mirror that is down stays down", len(ran), engineInstallTries)
	}
	if !strings.Contains(said, dockerSource) {
		t.Errorf("the refusal never names %s, so nothing says what was retried:\n%s", dockerSource, said)
	}
}

func TestTheWaitBetweenInstallAttemptsGrowsAndIsNotTheSameOnEveryHost(t *testing.T) {
	t.Parallel()

	spread := map[string]bool{}
	for range 6 {
		dir, _, paused := installer(t, 99)
		if said, err := installing(t, dir); err == nil {
			t.Fatalf("the engine step succeeded on a host whose install script never stood:\n%s", said)
		}
		waits := waited(t, paused)
		if len(waits) != engineInstallTries-1 {
			t.Fatalf("the engine step waited %v over %d attempts, want one wait between each pair", waits, engineInstallTries)
		}
		if waits[0] < engineInstallHold.base {
			t.Errorf("the first wait is %ds, want at least %ds: a mirror mid-sync needs longer than a round trip to finish it", waits[0], engineInstallHold.base)
		}
		if waits[1] <= waits[0] {
			t.Errorf("the second wait %ds is no longer than the first %ds, and a retry that does not back off hammers a mirror that is already refusing", waits[1], waits[0])
		}
		for _, held := range waits {
			if held > engineInstallHold.ceiling+engineInstallHold.spread {
				t.Errorf("the engine step waited %ds, past the %ds ceiling: an apply that stalls unboundedly is one nobody can time out", held, engineInstallHold.ceiling+engineInstallHold.spread)
			}
		}
		spread[fmt.Sprint(waits)] = true
	}
	if len(spread) < 2 {
		t.Errorf("six hosts retrying the same refusing mirror all waited the same %v, and a fleet that backs off in lockstep arrives back at the mirror together", spread)
	}
}

func TestAnInstallThatFailsSaysWhatItsLastTryPrintedFirst(t *testing.T) {
	t.Parallel()

	dir, _, _ := installerSaying(t, 99, `set -x
echo 'Get:1 http://us-east-1.ec2.archive.ubuntu.com/ubuntu noble InRelease'
sh -c 'apt-get -qq update >/dev/null' 2>/dev/null || true
{ set +x; } 2>/dev/null
echo 'E: Failed to fetch http://us-east-1.ec2.archive.ubuntu.com/ubuntu/dists/noble-updates/InRelease  Could not connect' >&2
echo 'E: Some index files failed to download.' >&2`)
	said, err := installing(t, dir)
	if err == nil {
		t.Fatalf("the engine step succeeded on a host whose install script never stood:\n%s", said)
	}
	lines := strings.Split(strings.TrimSpace(said), "\n")
	if len(lines) > saidLines {
		lines = lines[:saidLines]
	}
	shown := strings.Join(lines, "\n")
	for _, want := range []string{dockerSource, "E: Failed to fetch", "E: Some index files failed to download."} {
		if !strings.Contains(shown, want) {
			t.Errorf("the first %d lines a refusal shows are\n%s\nwant %q among them: an install that fails on a bad mirror has to say so where the user reads", saidLines, shown, want)
		}
	}
}

func TestEveryInstallAttemptIsBoundedAndSaysSoWhenTheBoundIsHit(t *testing.T) {
	t.Parallel()

	dir, attempts, _ := installer(t, 99)
	bounded := filepath.Join(dir, "bounded")
	executable(t, filepath.Join(dir, "timeout"), "#!/bin/sh\nprintf '%s\\n' \"$1\" >>"+quoted(bounded)+"\nexit 124\n")
	said, err := installing(t, dir)
	if err == nil {
		t.Fatalf("the engine step succeeded with every attempt timed out:\n%s", said)
	}
	read, err := os.ReadFile(bounded)
	if err != nil {
		t.Fatalf("no attempt ran under timeout, so an apt stall hangs the apply for as long as the mirror does: %v", err)
	}
	bounds := strings.Fields(string(read))
	if len(bounds) != engineInstallTries {
		t.Errorf("%d of %d attempts ran under timeout", len(bounds), engineInstallTries)
	}
	for _, bound := range bounds {
		if bound != strconv.Itoa(engineAttemptSeconds) {
			t.Errorf("an attempt was bounded at %q, want %d seconds", bound, engineAttemptSeconds)
		}
	}
	if ran, _ := os.ReadFile(attempts); len(ran) != 0 {
		t.Errorf("the install script ran %d times past a timeout that never let it start", len(ran))
	}
	if want := "timing out after " + strconv.Itoa(engineAttemptSeconds) + "s"; !strings.Contains(said, want) {
		t.Errorf("the refusal reads\n%s\nwant it to say %q: a stall reads as a failure of whatever ran last", said, want)
	}
}

func TestTheInstallBoundsHowLongAptWaitsOnAMirror(t *testing.T) {
	t.Parallel()

	configured := filepath.Join(t.TempDir(), "apt.conf")
	dir, _, _ := installerSaying(t, 0, `cat "$APT_CONFIG" >`+quoted(configured))
	if said, err := installing(t, dir); err != nil {
		t.Fatalf("the engine step = %v:\n%s", err, said)
	}
	read, err := os.ReadFile(configured)
	if err != nil {
		t.Fatalf("the install script ran with no apt config it could read: %v", err)
	}
	for _, want := range []string{`Acquire::http::Timeout "30";`, `Acquire::https::Timeout "30";`} {
		if !strings.Contains(string(read), want) {
			t.Errorf("the install script ran with an apt config of\n%s\nwant %s in it: apt's own timeout is two minutes a fetch, and a mirror answering Ign stalls it for twenty", read, want)
		}
	}
}
