//go:build integration

package enginetest

import (
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/images"
)

func TestMain(m *testing.M) { os.Exit(Main(m)) }

type planted struct {
	labelled, root string
}

func plant(t *testing.T, of string) planted {
	t.Helper()
	seen := filepath.Dir(filepath.Dir(BindSource(t)))
	left := planted{labelled: uniqueName("labelled")}
	does := func(argv ...string) {
		t.Helper()
		if said, err := exec.Command(engine, argv...).CombinedOutput(); err != nil {
			t.Fatalf("%s %s: %v\n%s", engine, strings.Join(argv, " "), err, said)
		}
	}
	t.Cleanup(func() {
		exec.Command(engine, "rm", "--force", "--volumes", left.labelled).Run()
	})
	defer sweepingAlone()()
	does(append(append([]string{"run", "--detach", "--name", left.labelled}, labelledAs(of)...),
		"--network", "none", "--entrypoint", "sleep", images.ObjectStore(), "600")...)

	root, err := os.MkdirTemp(seen, rootPrefix)
	if err != nil {
		t.Fatal(err)
	}
	left.root = root
	t.Cleanup(func() { removeRunRoot(root) })
	if err := os.WriteFile(filepath.Join(root, runFile), []byte(of), 0o644); err != nil {
		t.Fatal(err)
	}
	does("run", "--rm", "--network", "none", "--user", "0", "--volume", root+":/written",
		"--entrypoint", "sh", images.ObjectStore(), "-c", "mkdir -m 700 /written/data && echo written > /written/data/state")
	return left
}

func (p planted) remaining(t *testing.T) []string {
	t.Helper()
	var found []string
	if exec.Command(engine, "container", "inspect", p.labelled).Run() == nil {
		found = append(found, "container "+p.labelled)
	}
	if _, err := os.Stat(p.root); err == nil {
		found = append(found, "directory "+p.root)
	}
	return found
}

func TestWhatARunThatDiedLeftIsTakenByTheNextAndWhatALiveOneOwnsIsNot(t *testing.T) {
	RequireDocker(t)

	died := plant(t, aRunThatEnded(t))
	living := plant(t, runOf(hostname(), os.Getppid()))

	if err := sweep(runProcessGone); err != nil {
		t.Fatalf("sweep = %v", err)
	}
	if left := died.remaining(t); len(left) > 0 {
		t.Errorf("a run that died still has %v after the next run swept: every killed run then leaves its containers and the directories a root container wrote into for good", left)
	}
	if left := living.remaining(t); len(left) != 2 {
		t.Errorf("a run whose process still runs has only %v after another run swept, want its container and directory untouched: two suites running at once would take each other's fixtures mid-test", left)
	}
}

func TestTheStoreIsStartedOnceForTheWholeRunAndServesFromTheHostAndWithin(t *testing.T) {
	first := SharedObjectStore(t)
	if again := SharedObjectStore(t); again.Name != first.Name {
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
		t.Errorf("the store has %q as its run, want %q: an unlabelled store outlives a killed run", labelled, run)
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
		"--entrypoint", "test", images.ObjectStore(), "-f", "/seen/seen").CombinedOutput(); err != nil {
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
	t.Cleanup(func() { removeRunRoot(root) })
	if err := os.WriteFile(filepath.Join(root, runFile), []byte(aRunThatEnded(t)), 0o644); err != nil {
		t.Fatal(err)
	}
	guarded := filepath.Join(root, "data")
	if err := os.Mkdir(guarded, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(guarded, "state"), []byte("written\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(guarded, 0o555); err != nil {
		t.Fatal(err)
	}

	if err := emptyRunRoot(root); err == nil {
		t.Fatal("a root containing what this user cannot remove was emptied all the same")
	}
	if _, err := os.Stat(filepath.Join(root, runFile)); err != nil {
		t.Fatalf("the root lost the file naming its run while it still contained %s: when the engine cannot take the rest either, no later sweep can tell whose it is and it stays on the machine for good", guarded)
	}
	if err := removeRunRoot(root); err != nil {
		t.Fatalf("reclaimed = %v", err)
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Errorf("%s still exists after the engine took back what this user could not", root)
	}
}
