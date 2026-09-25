package enginetest

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/constants"
)

func TestMain(m *testing.M) { os.Exit(Main(m)) }

func aRunThatEnded(t *testing.T) string {
	t.Helper()
	gone := exec.Command(os.Args[0], "-test.run=^$")
	if said, err := gone.CombinedOutput(); err != nil {
		t.Fatalf("run a process to its end: %v\n%s", err, said)
	}
	return runOf(hostname(), gone.Process.Pid)
}

func TestARunHasEndedOnlyWhenItsProcessOnThisMachineHas(t *testing.T) {
	t.Parallel()

	for what, want := range map[string]struct {
		of    string
		ended bool
	}{
		"this run":                         {run, false},
		"a run whose process still stands": {runOf(hostname(), os.Getppid()), false},
		"a run whose process is gone":      {aRunThatEnded(t), true},
		"a run on another machine":         {runOf(hostname()+"-elsewhere", 1<<22), false},
		"a label nothing here wrote":       {"unreadable", false},
		"a pid that names no process":      {runOf(hostname(), 0), false},
	} {
		if got := ended(want.of); got != want.ended {
			t.Errorf("%s (%q): ended = %v, want %v: a live run swept loses what it stands on mid-test, and a dead one never swept leaves its containers and networks on the machine for good", what, want.of, got, want.ended)
		}
	}
}

func TestABucketOnTheSharedStoreIsTakenByOneTestAlone(t *testing.T) {
	t.Parallel()

	held := taken{by: map[string]string{}}
	if err := held.take("uploads", "TestFirst"); err != nil {
		t.Fatalf("the first take of a bucket = %v", err)
	}
	err := held.take("uploads", "TestSecond")
	if err == nil || !strings.Contains(err.Error(), "TestFirst") {
		t.Errorf("a second test taking the same bucket was told %v, want a refusal naming the test that holds it", err)
	}
	if err := held.take("uploads", "TestFirst"); err != nil {
		t.Errorf("the test holding a bucket was refused it again: %v", err)
	}
	if err := held.take("downloads", "TestSecond"); err != nil {
		t.Errorf("a bucket nothing holds was refused: %v", err)
	}
}

type planted struct {
	network, labelled, attached, root string
}

func plant(t *testing.T, of string) planted {
	t.Helper()
	seen := filepath.Dir(filepath.Dir(BindSource(t)))
	left := planted{network: named("net"), labelled: named("held"), attached: named("attached")}
	does := func(argv ...string) {
		t.Helper()
		if said, err := exec.Command(engine, argv...).CombinedOutput(); err != nil {
			t.Fatalf("%s %s: %v\n%s", engine, strings.Join(argv, " "), err, said)
		}
	}
	t.Cleanup(func() {
		exec.Command(engine, "rm", "--force", "--volumes", left.labelled, left.attached).Run()
		exec.Command(engine, "network", "rm", left.network).Run()
	})
	does(append(append([]string{"network", "create"}, labelledAs(of)...), left.network)...)
	does(append(append([]string{"run", "--detach", "--name", left.labelled}, labelledAs(of)...),
		"--network", "none", "--entrypoint", "sleep", constants.ObjectStoreImage(), "600")...)
	does("run", "--detach", "--name", left.attached, "--network", left.network,
		"--entrypoint", "sleep", constants.ObjectStoreImage(), "600")

	root, err := os.MkdirTemp(seen, rootPrefix)
	if err != nil {
		t.Fatal(err)
	}
	left.root = root
	t.Cleanup(func() { reclaimed(root) })
	if err := os.WriteFile(filepath.Join(root, runFile), []byte(of), 0o644); err != nil {
		t.Fatal(err)
	}
	does("run", "--rm", "--network", "none", "--user", "0", "--volume", root+":/written",
		"--entrypoint", "sh", constants.ObjectStoreImage(), "-c", "mkdir -m 700 /written/data && echo held > /written/data/state")
	return left
}

func (p planted) standing(t *testing.T) []string {
	t.Helper()
	var found []string
	for _, container := range []string{p.labelled, p.attached} {
		if exec.Command(engine, "container", "inspect", container).Run() == nil {
			found = append(found, "container "+container)
		}
	}
	if exec.Command(engine, "network", "inspect", p.network).Run() == nil {
		found = append(found, "network "+p.network)
	}
	if _, err := os.Stat(p.root); err == nil {
		found = append(found, "directory "+p.root)
	}
	return found
}

func TestWhatARunThatDiedLeftIsTakenByTheNextAndWhatALiveOneHoldsIsNot(t *testing.T) {
	engage(t)

	died := plant(t, aRunThatEnded(t))
	living := plant(t, runOf(hostname(), os.Getppid()))

	if err := sweep(ended); err != nil {
		t.Fatalf("sweep = %v", err)
	}
	if left := died.standing(t); len(left) > 0 {
		t.Errorf("a run that died still holds %v after the next run swept: every killed run then leaves its containers, its network and the directories a root container wrote into for good", left)
	}
	if left := living.standing(t); len(left) != 4 {
		t.Errorf("a run whose process still stands holds only %v after another run swept, want its containers, network and directory untouched: two suites running at once would take each other's fixtures mid-test", left)
	}
}

func TestTheStoreIsStoodOnceForTheWholeRunAndServesFromTheHostAndWithin(t *testing.T) {
	first := AStore(t)
	if again := AStore(t); again.Name != first.Name {
		t.Errorf("a second test was handed store %s, want %s: a store per test is a container, a volume and a veth per test", again.Name, first.Name)
	}
	said, err := http.Get(first.Endpoint + "/health/ready")
	if err != nil {
		t.Fatalf("ask the store from the host at %s: %v", first.Endpoint, err)
	}
	said.Body.Close()
	if said.StatusCode != http.StatusOK {
		t.Errorf("the store answered %d from the host", said.StatusCode)
	}
	if out, err := exec.Command(engine, "exec", first.Name, "curl", "-fsS", first.Inside+"/health/ready").CombinedOutput(); err != nil {
		t.Errorf("the store answered nothing from within at %s: %v\n%s", first.Inside, err, out)
	}
	labelled, err := exec.Command(engine, "inspect", "--format", `{{index .Config.Labels "`+runLabel+`"}}`, first.Name).Output()
	if err != nil || strings.TrimSpace(string(labelled)) != run {
		t.Errorf("the store carries %q as its run, want %q: an unlabelled store outlives a killed run", labelled, run)
	}
}

func TestABindSourceIsOneTheEngineReadsAndNoTwoTestsShareOne(t *testing.T) {
	first, second := BindSource(t), BindSource(t)
	if first == second {
		t.Fatalf("two bind sources were both %s", first)
	}
	if err := os.WriteFile(filepath.Join(first, "seen"), []byte("seen\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(engine, "run", "--rm", "--network", "none", "--user", "0", "--volume", first+":/seen:ro",
		"--entrypoint", "test", constants.ObjectStoreImage(), "-f", "/seen/seen").CombinedOutput(); err != nil {
		t.Errorf("the engine cannot read %s: %v\n%s", first, err, out)
	}
	if of, err := os.ReadFile(filepath.Join(filepath.Dir(first), runFile)); err != nil || string(of) != run {
		t.Errorf("the root over %s names run %q, want %q: a root that names no run is one no later run can sweep", first, of, run)
	}
}

func TestARootNamesItsRunUntilEverythingElseInItIsGone(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes what no permission guards, so nothing here can stand in for what a container wrote")
	}
	root, err := os.MkdirTemp(filepath.Dir(filepath.Dir(BindSource(t))), rootPrefix)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { reclaimed(root) })
	if err := os.WriteFile(filepath.Join(root, runFile), []byte(aRunThatEnded(t)), 0o644); err != nil {
		t.Fatal(err)
	}
	guarded := filepath.Join(root, "data")
	if err := os.Mkdir(guarded, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guarded, "state"), []byte("held\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(guarded, 0o555); err != nil {
		t.Fatal(err)
	}

	if err := emptied(root); err == nil {
		t.Fatal("a root holding what this user cannot remove was emptied all the same")
	}
	if _, err := os.Stat(filepath.Join(root, runFile)); err != nil {
		t.Fatalf("the root lost the file naming its run while it still held %s: when the engine cannot take the rest either, no later sweep can tell whose it is and it stays on the machine for good", guarded)
	}
	if err := reclaimed(root); err != nil {
		t.Fatalf("reclaimed = %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("%s still stands after the engine took back what this user could not", root)
	}
}

func TestARootAnotherRunAlreadySweptIsReclaimedWithoutComplaint(t *testing.T) {
	t.Parallel()

	gone := filepath.Join(t.TempDir(), rootPrefix+"swept")
	if err := reclaimed(gone); err != nil {
		t.Errorf("reclaimed(%s) = %v, want nil: two runs that sweep at once both list a root the first one then removes, and the second is left failing its whole run over a directory nobody holds any more", gone, err)
	}
}
