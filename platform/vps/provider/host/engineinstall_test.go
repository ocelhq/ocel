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
	dir := t.TempDir()
	attempts := filepath.Join(dir, "attempts")
	fetched := filepath.Join(dir, "fetched")
	paused := filepath.Join(dir, "paused")

	executable(t, fetched, "#!/bin/sh\nprintf a >>"+quoted(attempts)+"\n"+
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
		if waits[0] < engineInstallBackoffSeconds {
			t.Errorf("the first wait is %ds, want at least %ds: a mirror mid-sync needs longer than a round trip to finish it", waits[0], engineInstallBackoffSeconds)
		}
		if waits[1] <= waits[0] {
			t.Errorf("the second wait %ds is no longer than the first %ds, and a retry that does not back off hammers a mirror that is already refusing", waits[1], waits[0])
		}
		for _, held := range waits {
			if held > engineInstallCeilingSeconds+engineInstallJitterSeconds {
				t.Errorf("the engine step waited %ds, past the %ds ceiling: an apply that stalls unboundedly is one nobody can time out", held, engineInstallCeilingSeconds+engineInstallJitterSeconds)
			}
		}
		spread[fmt.Sprint(waits)] = true
	}
	if len(spread) < 2 {
		t.Errorf("six hosts retrying the same refusing mirror all waited the same %v, and a fleet that backs off in lockstep arrives back at the mirror together", spread)
	}
}
