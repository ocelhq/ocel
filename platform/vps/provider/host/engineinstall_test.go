package host

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
	"github.com/ocelhq/ocel/platform/vps/provider/session"
)

func installer(t *testing.T, failures int) (string, string) {
	t.Helper()
	return installerSaying(t, failures, "")
}

func installerSaying(t *testing.T, failures int, said string) (string, string) {
	t.Helper()
	dir := t.TempDir()
	attempts := filepath.Join(dir, "attempts")
	fetched := filepath.Join(dir, "fetched")

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
	return dir, attempts
}

func installing(t *testing.T, dir string) (string, error) {
	t.Helper()
	result := installedOn(dir, engineCommand())
	if result.Code != 0 {
		return result.Stderr, fmt.Errorf("exit status %d", result.Code)
	}
	return result.Stderr, nil
}

func installedOn(dir, command string) session.Result {
	cmd := exec.Command("/bin/sh", "-c", command)
	cmd.Env = append(os.Environ(), "PATH="+dir+":"+os.Getenv("PATH"))
	var stdout, stderr strings.Builder
	cmd.Stdout, cmd.Stderr = &stdout, &stderr
	if err := cmd.Run(); err != nil {
		return session.Result{Stdout: stdout.String(), Stderr: stderr.String(), Code: cmd.ProcessState.ExitCode()}
	}
	return session.Result{Stdout: stdout.String(), Stderr: stderr.String()}
}

type told struct {
	mu      sync.Mutex
	details []string
}

func (r *told) Say(string) {}

func (r *told) Detail(message string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.details = append(r.details, message)
}

func (r *told) Span(string, time.Time, time.Time, error, ...edge.Attr) {}

func (r *told) said() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.details...)
}

func installsOn(dir string, progress *told) (*bench, *[]int) {
	stood := machine(nil)
	var toldBefore []int
	stood.answer = func(command string) (session.Result, bool) {
		if !strings.Contains(command, dockerSource) {
			return session.Result{}, false
		}
		toldBefore = append(toldBefore, len(progress.said()))
		return installedOn(dir, command), true
	}
	return stood, &toldBefore
}

func TestAnInstallScriptThatFailedTwiceIsRunAgainRatherThanFailingTheApply(t *testing.T) {
	t.Parallel()

	dir, attempts := installer(t, 2)
	stood, _ := installsOn(dir, &told{})
	if err := stood.host().installEngine(context.Background(), nil); err != nil {
		t.Fatalf("installEngine() = %v on a host whose install script failed twice and then stood", err)
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

	dir, attempts := installer(t, 99)
	stood, _ := installsOn(dir, &told{})
	refused := refusalOf(t, stood.host().installEngine(context.Background(), nil), refusal.CodeNotReady)
	ran, err := os.ReadFile(attempts)
	if err != nil {
		t.Fatal(err)
	}
	if len(ran) != engineInstallTries {
		t.Errorf("the install script ran %d times, want it bounded at %d: a mirror that is down stays down", len(ran), engineInstallTries)
	}
	if !strings.Contains(refused.Message, dockerSource) {
		t.Errorf("the refusal never names %s, so nothing says what was retried:\n%s", dockerSource, refused.Message)
	}
}

func TestTheWaitBetweenInstallAttemptsGrowsAndIsNotTheSameOnEveryHost(t *testing.T) {
	t.Parallel()

	spread := map[string]bool{}
	for range 6 {
		dir, _ := installer(t, 99)
		stood, _ := installsOn(dir, &told{})
		if err := stood.host().installEngine(context.Background(), nil); err == nil {
			t.Fatal("installEngine() stood on a host whose install script never stood")
		}
		waits := stood.waits()
		if len(waits) != engineInstallTries-1 {
			t.Fatalf("the engine step waited %v over %d attempts, want one wait between each pair", waits, engineInstallTries)
		}
		if floor := time.Duration(engineInstallHold.base) * time.Second; waits[0] < floor {
			t.Errorf("the first wait is %s, want at least %s: a mirror mid-sync needs longer than a round trip to finish it", waits[0], floor)
		}
		if waits[1] <= waits[0] {
			t.Errorf("the second wait %s is no longer than the first %s, and a retry that does not back off hammers a mirror that is already refusing", waits[1], waits[0])
		}
		for _, held := range waits {
			if ceiling := time.Duration(engineInstallHold.ceiling+engineInstallHold.spread) * time.Second; held > ceiling {
				t.Errorf("the engine step waited %s, past the %s ceiling: an apply that stalls unboundedly is one nobody can time out", held, ceiling)
			}
		}
		spread[fmt.Sprint(waits)] = true
	}
	if len(spread) < 2 {
		t.Errorf("six hosts retrying the same refusing mirror all waited the same %v, and a fleet that backs off in lockstep arrives back at the mirror together", spread)
	}
}

func TestEveryFailedInstallTryIsToldBeforeTheNextOneRuns(t *testing.T) {
	t.Parallel()

	dir, _ := installerSaying(t, 99, `echo 'E: Failed to fetch http://us-east-1.ec2.archive.ubuntu.com/ubuntu/dists/noble-updates/InRelease  Could not connect' >&2
echo 'E: Some index files failed to download.' >&2`)
	progress := &told{}
	stood, toldBefore := installsOn(dir, progress)
	if err := stood.host().installEngine(context.Background(), progress); err == nil {
		t.Fatal("installEngine() stood on a host whose install script never stood")
	}
	details := progress.said()
	if len(*toldBefore) != engineInstallTries {
		t.Fatalf("the install tried %d times, want %d", len(*toldBefore), engineInstallTries)
	}
	for try := 1; try < engineInstallTries; try++ {
		before := details[(*toldBefore)[try-1]:(*toldBefore)[try]]
		for _, want := range []string{fmt.Sprintf("try %d of %d", try, engineInstallTries), "E: Some index files failed to download."} {
			if !slices.ContainsFunc(before, func(detail string) bool { return strings.Contains(detail, want) }) {
				t.Errorf("try %d ran after the install told\n%s\nwant %q among it: a user watching a twenty-minute install hears nothing until the last try fails", try+1, strings.Join(before, "\n"), want)
			}
		}
	}
	for _, detail := range details {
		if strings.Contains(detail, "\n") {
			t.Errorf("the install told %q, and a detail loses its newlines on the way to the user", detail)
		}
	}
}

func TestAnInstallSudoRefusedIsNotTriedAgain(t *testing.T) {
	t.Parallel()

	stood := machine(nil)
	stood.facts.Root, stood.facts.Sudo = false, true
	stood.answer = func(command string) (session.Result, bool) {
		return session.Result{Code: 1, Stderr: "sudo: a password is required"}, strings.Contains(command, dockerSource)
	}
	refusalOf(t, stood.host().installEngine(context.Background(), nil), refusal.CodeDenied)
	tries := 0
	for _, command := range stood.commands() {
		if strings.Contains(command, dockerSource) {
			tries++
		}
	}
	if tries != 1 {
		t.Errorf("the install tried %d times against a sudo that refused the login, want once: sudo does not change its mind between tries", tries)
	}
	if waits := stood.waits(); len(waits) != 0 {
		t.Errorf("the install waited %v after a sudo refusal before refusing", waits)
	}
}

func TestAnInstallThatFailsSaysWhatItsLastTryPrintedFirst(t *testing.T) {
	t.Parallel()

	dir, _ := installerSaying(t, 99, `set -x
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

func TestNoInstallAttemptRunsUnderAKillThatWouldInterruptDpkg(t *testing.T) {
	t.Parallel()

	dir, attempts := installer(t, 0)
	killed := filepath.Join(dir, "killed")
	executable(t, filepath.Join(dir, "timeout"), "#!/bin/sh\nprintf '%s\\n' \"$*\" >>"+quoted(killed)+"\nexit 124\n")
	if said, err := installing(t, dir); err != nil {
		t.Fatalf("the engine step = %v:\n%s", err, said)
	}
	if ran, err := os.ReadFile(killed); err == nil {
		t.Errorf("an attempt ran under `timeout %s`, which signals the whole process group: a kill that lands while dpkg unpacks leaves dpkg interrupted and docker half-installed on the box", strings.TrimSpace(string(ran)))
	}
	if ran, _ := os.ReadFile(attempts); len(ran) != 1 {
		t.Errorf("the install script ran %d times, want once", len(ran))
	}
}

func TestTheInstallBoundsEveryWaitItCanStallOn(t *testing.T) {
	t.Parallel()

	configured := filepath.Join(t.TempDir(), "apt.conf")
	fetching := filepath.Join(t.TempDir(), "curlrc")
	dir, _ := installerSaying(t, 0, `cat "$APT_CONFIG" >`+quoted(configured)+`
cat "$CURL_HOME/.curlrc" >`+quoted(fetching))
	if said, err := installing(t, dir); err != nil {
		t.Fatalf("the engine step = %v:\n%s", err, said)
	}
	for file, wants := range map[string][]string{
		configured: {
			`Acquire::http::Timeout "30";`,
			`Acquire::https::Timeout "30";`,
			`DPkg::Lock::Timeout "300";`,
		},
		fetching: {"connect-timeout = 30", "max-time = 120"},
	} {
		read, err := os.ReadFile(file)
		if err != nil {
			t.Fatalf("the install script ran with nothing it could read at %s: %v", file, err)
		}
		for _, want := range wants {
			if !strings.Contains(string(read), want) {
				t.Errorf("the install script ran with\n%s\nwant %s in it: apt waits two minutes a fetch by default and forever on a held dpkg lock, and curl waits forever on a stalled transfer", read, want)
			}
		}
	}
}
