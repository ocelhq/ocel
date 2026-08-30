package host

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func releasesDir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("no flock on this machine, and the helper takes its lock with it")
	}
	return t.TempDir()
}

func releases(t *testing.T, root, path, app string, args ...string) (string, int) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "releases")
	if err := os.WriteFile(script, releasesScript, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", append([]string{script, app}, args...)...)
	cmd.Env = append(os.Environ(), "OCEL_RELEASES_ROOT="+root)
	if path != "" {
		cmd.Env = append(cmd.Env, "PATH="+path+":"+os.Getenv("PATH"))
	}
	var stderr strings.Builder
	cmd.Stderr = &stderr
	rendered, err := cmd.Output()
	var exit *exec.ExitError
	switch {
	case err == nil:
		return string(rendered), 0
	case errors.As(err, &exit):
		return string(rendered), exit.ExitCode()
	default:
		t.Fatalf("run the releases helper: %v\n%s", err, stderr.String())
		return "", 0
	}
}

func promote(t *testing.T, root, app, class, ref string) {
	t.Helper()
	if _, code := releases(t, root, "", app, "promote", class, ref); code != 0 {
		t.Fatalf("promote %s exited %d", ref, code)
	}
}

func window(t *testing.T, root, app, class string) []string {
	t.Helper()
	held, err := os.ReadFile(filepath.Join(root, app, class))
	if err != nil {
		t.Fatal(err)
	}
	trimmed := strings.TrimRight(string(held), "\n")
	if trimmed == "" {
		return nil
	}
	return strings.Split(trimmed, "\n")
}

func TestTheWindowKeepsThreeMostRecentlyServedFirst(t *testing.T) {
	t.Parallel()

	root := releasesDir(t)
	for _, ref := range []string{"ocel/web:one", "ocel/web:two", "ocel/web:three", "ocel/web:four"} {
		promote(t, root, "web", "production", ref)
	}
	held := window(t, root, "web", "production")
	want := []string{"ocel/web:four", "ocel/web:three", "ocel/web:two"}
	if strings.Join(held, ",") != strings.Join(want, ",") {
		t.Errorf("the window holds %v, want %v: the head is the most recently served and the fourth ref evicts the first", held, want)
	}
}

func TestARepeatedRefMovesToTheHeadRatherThanConsumingASlot(t *testing.T) {
	t.Parallel()

	root := releasesDir(t)
	for _, ref := range []string{"ocel/web:a", "ocel/web:b", "ocel/web:c", "ocel/web:a"} {
		promote(t, root, "web", "production", ref)
	}
	held := window(t, root, "web", "production")
	want := []string{"ocel/web:a", "ocel/web:c", "ocel/web:b"}
	if strings.Join(held, ",") != strings.Join(want, ",") {
		t.Errorf("the window holds %v, want %v: re-promoting a retained ref moves it to the head and evicts nothing", held, want)
	}
}

func TestTheWindowFileNeverGrows(t *testing.T) {
	t.Parallel()

	root := releasesDir(t)
	for i := range 50 {
		promote(t, root, "web", "production", "ocel/web:r"+strconv.Itoa(i))
		if held := window(t, root, "web", "production"); len(held) > 3 {
			t.Fatalf("after %d promotes the window holds %d refs, and a file that grows is a read whose size is a function of history", i+1, len(held))
		}
	}
	alone := releasesDir(t)
	for range 50 {
		promote(t, alone, "web", "production", "ocel/web:same")
	}
	if held := window(t, alone, "web", "production"); len(held) != 1 || held[0] != "ocel/web:same" {
		t.Errorf("fifty promotes of one ref left %v, want that ref alone: the window is not an append log", held)
	}
}

func TestAPromoteRenamesAndLeavesNothingHalfWritten(t *testing.T) {
	t.Parallel()

	root := releasesDir(t)
	promote(t, root, "web", "production", "ocel/web:one")
	entries, err := os.ReadDir(filepath.Join(root, "web"))
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range entries {
		if entry.Name() != "production" {
			t.Errorf("%s stands beside the window, and a write that renames leaves nothing behind", entry.Name())
		}
	}
}

func TestForgettingAClassLeavesTheBoxAsItStoodBeforeTheFirstPromote(t *testing.T) {
	t.Parallel()

	root := releasesDir(t)
	promote(t, root, "web", "production", "ocel/web:one")
	if _, code := releases(t, root, "", "web", "forget", "production"); code != 0 {
		t.Fatalf("forget exited %d", code)
	}
	if _, err := os.Stat(filepath.Join(root, "web")); !os.IsNotExist(err) {
		t.Errorf("the app directory stands after its last class was forgotten (%v), and a teardown reclaims the bytes its own deploys wrote", err)
	}
}

func TestForgettingOneClassLeavesTheOthersStanding(t *testing.T) {
	t.Parallel()

	root := releasesDir(t)
	promote(t, root, "web", "production", "ocel/web:live")
	promote(t, root, "web", "preview", "ocel/web:branch")
	if _, code := releases(t, root, "", "web", "forget", "preview"); code != 0 {
		t.Fatalf("forget exited %d", code)
	}
	if held := window(t, root, "web", "production"); len(held) != 1 || held[0] != "ocel/web:live" {
		t.Errorf("forgetting preview left production holding %v, and one class's teardown is never another's", held)
	}
}

func TestForgettingAClassTheHostNeverServedIsRefused(t *testing.T) {
	t.Parallel()

	root := releasesDir(t)
	for _, class := range []string{"", "../../etc", "PRODUCTION", "prod;rm -rf /"} {
		if _, code := releases(t, root, "", "web", "forget", class); code != 2 {
			t.Errorf("forget %q exited %d, want a refusal", class, code)
		}
	}
}

func TestTheHelperRefusesAnAppNameItCannotName(t *testing.T) {
	t.Parallel()

	root := releasesDir(t)
	for _, app := range []string{"web; rm -rf /", "$(id)", "'; docker rmi $(docker images -q); #", "../../etc", "web\nweb", "WEB"} {
		rendered, code := releases(t, root, "", app, "promote", "production", "ocel/web:one")
		if code != 2 {
			t.Errorf("promote as %q exited %d, want a refusal", app, code)
		}
		if rendered != "" {
			t.Errorf("promote as %q wrote %q to stdout", app, rendered)
		}
	}
	if _, err := os.Stat(filepath.Join(root, "web")); err == nil {
		t.Error("a refused app name still wrote a window, and the gate is what stops a name reaching the filesystem at all")
	}
}

func TestTheHelperRefusesACoordinateItCannotName(t *testing.T) {
	t.Parallel()

	root := releasesDir(t)
	for _, ref := range []string{"", "-rf", "ocel/web:one; docker rmi $(docker images -q)", "ocel/web:one\nocel/web:two"} {
		if _, code := releases(t, root, "", "web", "promote", "production", ref); code != 2 {
			t.Errorf("promote %q exited %d, want a refusal", ref, code)
		}
	}
}

func TestOneAppsWindowsAreReadTogetherAndAnothersAreNeverRead(t *testing.T) {
	root := releasesDir(t)
	promote(t, root, "web", "production", "ocel/web:live")
	for _, ref := range []string{"ocel/web:p1", "ocel/web:p2", "ocel/web:p3"} {
		promote(t, root, "web", "preview", ref)
	}
	promote(t, root, "api", "production", "ocel/api:live")

	dock := fakeDocker(t, nil, []string{"ocel/web:live", "ocel/web:p1", "ocel/web:p2", "ocel/web:p3", "ocel/web:gone"})
	rendered, code := dock.reconcile(t, root, "web", "ocel/web")
	if code != 0 {
		t.Fatalf("reconcile exited %d", code)
	}
	if swept(rendered) != "ocel/web:gone" {
		t.Errorf("reconcile removed %q, want ocel/web:gone alone: preview churn cannot evict what production still names", swept(rendered))
	}
	if said := dock.listed(t); !strings.Contains(said, "reference=ocel/web:*") {
		t.Errorf("the image listing ran as %q, and actual is one listing filtered to a single app", said)
	}
	if strings.Contains(dock.log(t), "ocel/api") {
		t.Error("the sweep of web named api at all, and the filter and the desired set are computed over one scope")
	}
}

func TestARefOnlyARunningContainerNamesIsNeverRemoved(t *testing.T) {
	root := releasesDir(t)
	promote(t, root, "web", "production", "ocel/web:named")

	dock := fakeDocker(t, []string{"ocel/web:running"}, []string{"ocel/web:named", "ocel/web:running", "ocel/web:orphan"})
	rendered, code := dock.reconcile(t, root, "web", "ocel/web")
	if code != 0 {
		t.Fatalf("reconcile exited %d", code)
	}
	if swept(rendered) != "ocel/web:orphan" {
		t.Errorf("reconcile removed %q, want ocel/web:orphan alone: a ref no window names is still held by the container serving it", swept(rendered))
	}
}

func TestAContainerCarryingTheAppLabelAndNoRefStopsTheSweep(t *testing.T) {
	root := releasesDir(t)
	promote(t, root, "web", "production", "ocel/web:named")

	dock := fakeDocker(t, []string{""}, []string{"ocel/web:named", "ocel/web:orphan"})
	rendered, code := dock.reconcile(t, root, "web", "ocel/web")
	if code != 2 {
		t.Errorf("reconcile exited %d, want a refusal: a container ocel labelled and cannot name is a broken box", code)
	}
	if rendered != "" {
		t.Errorf("reconcile removed %q while it could not say what was running", rendered)
	}
	if strings.Contains(dock.log(t), "rmi") {
		t.Error("the sweep removed an image while it could not read what was running")
	}
}

func TestASecondReconcileRemovesNothing(t *testing.T) {
	root := releasesDir(t)
	promote(t, root, "web", "production", "ocel/web:named")

	dock := fakeDocker(t, nil, []string{"ocel/web:named", "ocel/web:orphan"})
	if _, code := dock.reconcile(t, root, "web", "ocel/web"); code != 0 {
		t.Fatal("the first reconcile refused")
	}
	rendered, code := dock.reconcile(t, root, "web", "ocel/web")
	if code != 0 {
		t.Fatalf("reconcile exited %d", code)
	}
	if rendered != "" {
		t.Errorf("a second reconcile with no deploy between removed %q, want nothing", rendered)
	}
	if got := dock.forced(t); got != "" {
		t.Errorf("the sweep ran %q: removal is never forced and never a prune", got)
	}
}

func TestARefusedRemovalIsLeftForTheNextRun(t *testing.T) {
	root := releasesDir(t)
	dock := fakeDocker(t, nil, []string{"ocel/web:held"})
	dock.refuse(t)

	rendered, code := dock.reconcile(t, root, "web", "ocel/web")
	if code != 0 {
		t.Fatalf("reconcile exited %d, want a daemon that refuses to leave the run successful", code)
	}
	if rendered != "" {
		t.Errorf("reconcile reported %q removed, and a daemon that refused removed nothing", rendered)
	}
	if !strings.Contains(dock.log(t), "rmi ocel/web:held") {
		t.Error("reconcile never asked for the removal at all")
	}
}

func swept(rendered string) string { return strings.TrimSpace(rendered) }

type dockerStub struct{ dir string }

func fakeDocker(t *testing.T, running, images []string) dockerStub {
	t.Helper()
	dir := t.TempDir()
	for name, lines := range map[string][]string{"running": running, "images": images} {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(joined(lines)), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	script := "#!/bin/sh\n" +
		"printf '%s\\n' \"$*\" >>" + filepath.Join(dir, "log") + "\n" +
		"case \"$1\" in\n" +
		"ps) cat " + filepath.Join(dir, "running") + " ;;\n" +
		"images) cat " + filepath.Join(dir, "images") + " ;;\n" +
		"rmi) if [ -f " + filepath.Join(dir, "refuse") + " ]; then echo 'image is in use' >&2; exit 1; fi\n" +
		"    grep -F -x -v -e \"$2\" " + filepath.Join(dir, "images") + " >" + filepath.Join(dir, "images.next") + " || true\n" +
		"    mv -f " + filepath.Join(dir, "images.next") + " " + filepath.Join(dir, "images") + " ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dockerStub{dir: dir}
}

func (d dockerStub) reconcile(t *testing.T, root, app, repository string) (string, int) {
	t.Helper()
	return releases(t, root, d.dir, app, "reconcile", repository)
}

func (d dockerStub) refuse(t *testing.T) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(d.dir, "refuse"), nil, 0o644); err != nil {
		t.Fatal(err)
	}
}

func (d dockerStub) log(t *testing.T) string {
	t.Helper()
	held, err := os.ReadFile(filepath.Join(d.dir, "log"))
	if err != nil {
		t.Fatalf("the sweep ran no docker command at all: %v", err)
	}
	return string(held)
}

func (d dockerStub) listed(t *testing.T) string {
	t.Helper()
	for line := range strings.Lines(d.log(t)) {
		if strings.HasPrefix(line, "images ") {
			return strings.TrimSpace(line)
		}
	}
	t.Fatal("the sweep listed no images")
	return ""
}

func (d dockerStub) forced(t *testing.T) string {
	t.Helper()
	for line := range strings.Lines(d.log(t)) {
		if strings.Contains(line, "--force") || strings.Contains(line, " -f") || strings.Contains(line, "prune") {
			return strings.TrimSpace(line)
		}
	}
	return ""
}

func joined(lines []string) string {
	if len(lines) == 0 {
		return ""
	}
	return strings.Join(lines, "\n") + "\n"
}
