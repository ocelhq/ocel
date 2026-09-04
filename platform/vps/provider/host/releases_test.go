package host

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"
)

func releasesDir(t *testing.T) string {
	t.Helper()
	if _, err := exec.LookPath("flock"); err != nil {
		t.Skip("no flock on this machine, and the helper takes its lock with it")
	}
	return t.TempDir()
}

func releases(t *testing.T, root, path, scope string, args ...string) (string, int) {
	t.Helper()
	script := filepath.Join(t.TempDir(), "releases")
	if err := os.WriteFile(script, releasesScript, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", append([]string{script, scope}, args...)...)
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

func promote(t *testing.T, root, scope, class, ref string) {
	t.Helper()
	if _, code := releases(t, root, "", scope, "promote", class, ref); code != 0 {
		t.Fatalf("promote %s exited %d", ref, code)
	}
}

func window(t *testing.T, root, scope, class string) []string {
	t.Helper()
	held, err := os.ReadFile(filepath.Join(root, filepath.FromSlash(scope), class))
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
	for _, ref := range []string{"ocel/shop/web:one", "ocel/shop/web:two", "ocel/shop/web:three", "ocel/shop/web:four"} {
		promote(t, root, "shop/web", "production", ref)
	}
	held := window(t, root, "shop/web", "production")
	want := []string{"ocel/shop/web:four", "ocel/shop/web:three", "ocel/shop/web:two"}
	if strings.Join(held, ",") != strings.Join(want, ",") {
		t.Errorf("the window holds %v, want %v: the head is the most recently served and the fourth ref evicts the first", held, want)
	}
}

func TestARepeatedRefMovesToTheHeadRatherThanConsumingASlot(t *testing.T) {
	t.Parallel()

	root := releasesDir(t)
	for _, ref := range []string{"ocel/shop/web:a", "ocel/shop/web:b", "ocel/shop/web:c", "ocel/shop/web:a"} {
		promote(t, root, "shop/web", "production", ref)
	}
	held := window(t, root, "shop/web", "production")
	want := []string{"ocel/shop/web:a", "ocel/shop/web:c", "ocel/shop/web:b"}
	if strings.Join(held, ",") != strings.Join(want, ",") {
		t.Errorf("the window holds %v, want %v: re-promoting a retained ref moves it to the head and evicts nothing", held, want)
	}
}

func TestTheWindowFileNeverGrows(t *testing.T) {
	t.Parallel()

	root := releasesDir(t)
	for i := range 50 {
		promote(t, root, "shop/web", "production", "ocel/shop/web:r"+strconv.Itoa(i))
		if held := window(t, root, "shop/web", "production"); len(held) > 3 {
			t.Fatalf("after %d promotes the window holds %d refs, and a file that grows is a read whose size is a function of history", i+1, len(held))
		}
	}
	alone := releasesDir(t)
	for range 50 {
		promote(t, alone, "shop/web", "production", "ocel/shop/web:same")
	}
	if held := window(t, alone, "shop/web", "production"); len(held) != 1 || held[0] != "ocel/shop/web:same" {
		t.Errorf("fifty promotes of one ref left %v, want that ref alone: the window is not an append log", held)
	}
}

func TestAPromoteRenamesAndLeavesNothingHalfWritten(t *testing.T) {
	t.Parallel()

	root := releasesDir(t)
	file := filepath.Join(root, "shop", "web", "production")
	promote(t, root, "shop/web", "production", "ocel/shop/web:one")
	first, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	promote(t, root, "shop/web", "production", "ocel/shop/web:two")
	second, err := os.Stat(file)
	if err != nil {
		t.Fatal(err)
	}
	if os.SameFile(first, second) {
		t.Error("the window was rewritten in place, and a reader between the truncate and the write reads a window naming nothing: a sweep that reads it there removes every image the app is serving")
	}
	staged, err := filepath.Glob(filepath.Join(root, ".staging.*"))
	if err != nil {
		t.Fatal(err)
	}
	if len(staged) != 0 {
		t.Errorf("%v stands where the helper stages, and a write that renames leaves nothing behind", staged)
	}
}

func TestConcurrentPromotesOfOneClassLoseNoUpdate(t *testing.T) {
	t.Parallel()

	script := filepath.Join(t.TempDir(), "releases")
	if err := os.WriteFile(script, releasesScript, 0o755); err != nil {
		t.Fatal(err)
	}
	for round := range 20 {
		root := releasesDir(t)
		refs := []string{"ocel/shop/web:a", "ocel/shop/web:b", "ocel/shop/web:c"}
		var racing sync.WaitGroup
		failed := make([]error, len(refs))
		for at, ref := range refs {
			racing.Add(1)
			go func() {
				defer racing.Done()
				cmd := exec.Command("/bin/sh", script, "shop/web", "promote", "production", ref)
				cmd.Env = append(os.Environ(), "OCEL_RELEASES_ROOT="+root)
				failed[at] = cmd.Run()
			}()
		}
		racing.Wait()
		for at, err := range failed {
			if err != nil {
				t.Fatalf("promote %s exited: %v", refs[at], err)
			}
		}
		held := window(t, root, "shop/web", "production")
		slices.Sort(held)
		if !slices.Equal(held, refs) {
			t.Fatalf("round %d left the window holding %v, want all of %v: promote reads the window, prepends and renames, and two of those interleaving without the lock drops whichever landed first", round, held, refs)
		}
	}
}

func slowGrep(t *testing.T) string {
	t.Helper()
	real, err := exec.LookPath("grep")
	if err != nil {
		t.Skip("no grep on this machine")
	}
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "grep"),
		[]byte("#!/bin/sh\nsleep 5\nexec "+real+" \"$@\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return dir
}

func staging(t *testing.T, root string) []string {
	t.Helper()
	found, err := filepath.Glob(filepath.Join(root, ".staging.*"))
	if err != nil {
		t.Fatal(err)
	}
	return found
}

func TestAPromoteKilledMidWriteTakesItsStagingFileWithIt(t *testing.T) {
	root := releasesDir(t)
	script := filepath.Join(t.TempDir(), "releases")
	if err := os.WriteFile(script, releasesScript, 0o755); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command("/bin/sh", script, "shop/web", "promote", "production", "ocel/shop/web:one")
	cmd.Env = append(os.Environ(), "OCEL_RELEASES_ROOT="+root, "PATH="+slowGrep(t)+":"+os.Getenv("PATH"))
	cmd.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := cmd.Start(); err != nil {
		t.Fatal(err)
	}
	for range 200 {
		if len(staging(t, root)) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(staging(t, root)) == 0 {
		t.Fatal("the promote reached its write without staging anything, and this test has nothing to interrupt")
	}
	if err := syscall.Kill(-cmd.Process.Pid, syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}
	_ = cmd.Wait()
	if left := staging(t, root); len(left) != 0 {
		t.Errorf("%v stands after the helper was killed, and a cli whose ssh channel dies delivers exactly this: a trap on EXIT alone never runs, so every interrupted deploy leaves a file no later run removes", left)
	}
}

func TestOneAppsSlowWriteNeverHoldsAnotherAppUp(t *testing.T) {
	root := releasesDir(t)
	script := filepath.Join(t.TempDir(), "releases")
	if err := os.WriteFile(script, releasesScript, 0o755); err != nil {
		t.Fatal(err)
	}
	slow := exec.Command("/bin/sh", script, "shop/web", "promote", "production", "ocel/shop/web:one")
	slow.Env = append(os.Environ(), "OCEL_RELEASES_ROOT="+root, "PATH="+slowGrep(t)+":"+os.Getenv("PATH"))
	slow.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	if err := slow.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		_ = syscall.Kill(-slow.Process.Pid, syscall.SIGKILL)
		_ = slow.Wait()
	})
	for range 200 {
		if len(staging(t, root)) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(staging(t, root)) == 0 {
		t.Fatal("web's promote never reached its write, so it is holding no lock and this test proves nothing")
	}

	done := make(chan error, 1)
	go func() {
		other := exec.Command("/bin/sh", script, "shop/api", "promote", "production", "ocel/shop/api:one")
		other.Env = append(os.Environ(), "OCEL_RELEASES_ROOT="+root)
		done <- other.Run()
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatalf("api's promote exited: %v", err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("api's promote was still waiting while web was mid-write: one lock over the whole release root serialises every app on the box, and it is held across the sweep's every docker rmi")
	}
}

func TestAPromoteReclaimsTheStagingFilesOfHelpersNoLongerRunning(t *testing.T) {
	root := releasesDir(t)
	dead := filepath.Join(root, ".staging.4294967290.staged")
	live := filepath.Join(root, ".staging."+strconv.Itoa(os.Getpid())+".staged")
	for _, name := range []string{dead, live} {
		if err := os.WriteFile(name, nil, 0o600); err != nil {
			t.Fatal(err)
		}
	}
	promote(t, root, "shop/web", "production", "ocel/shop/web:one")
	if _, err := os.Stat(dead); !os.IsNotExist(err) {
		t.Errorf("%s stands after a later run held the lock (%v), and nothing else on this box ever sweeps it", dead, err)
	}
	if _, err := os.Stat(live); err != nil {
		t.Errorf("the sweep took %s, whose helper is still running: a staging file is only stale once its writer is gone", live)
	}
}

func TestForgettingAClassLeavesTheBoxAsItStoodBeforeTheFirstPromote(t *testing.T) {
	t.Parallel()

	root := releasesDir(t)
	promote(t, root, "shop/web", "production", "ocel/shop/web:one")
	if _, code := releases(t, root, "", "shop/web", "forget", "production"); code != 0 {
		t.Fatalf("forget exited %d", code)
	}
	for _, left := range []string{filepath.Join(root, "shop", "web"), filepath.Join(root, "shop")} {
		if _, err := os.Stat(left); !os.IsNotExist(err) {
			t.Errorf("%s stands after the last class was forgotten (%v), and a teardown reclaims the bytes its own deploys wrote", left, err)
		}
	}
}

func TestForgettingOneClassLeavesTheOthersStanding(t *testing.T) {
	t.Parallel()

	root := releasesDir(t)
	promote(t, root, "shop/web", "production", "ocel/shop/web:live")
	promote(t, root, "shop/web", "preview", "ocel/shop/web:branch")
	if _, code := releases(t, root, "", "shop/web", "forget", "preview"); code != 0 {
		t.Fatalf("forget exited %d", code)
	}
	if held := window(t, root, "shop/web", "production"); len(held) != 1 || held[0] != "ocel/shop/web:live" {
		t.Errorf("forgetting preview left production holding %v, and one class's teardown is never another's", held)
	}
}

func TestForgettingAClassTheHostNeverServedIsRefused(t *testing.T) {
	t.Parallel()

	root := releasesDir(t)
	for _, class := range []string{"", "../../etc", "PRODUCTION", "prod;rm -rf /"} {
		if _, code := releases(t, root, "", "shop/web", "forget", class); code != 2 {
			t.Errorf("forget %q exited %d, want a refusal", class, code)
		}
	}
}

func TestTheHelperRefusesAScopeThatIsNotOneProjectAndOneApp(t *testing.T) {
	t.Parallel()

	root := releasesDir(t)
	for _, scope := range []string{
		"shop/web; rm -rf /", "$(id)", "'; docker rmi $(docker images -q); #", "../../etc", "shop/web\nshop/web", "SHOP/WEB",
		"web", "shop/web/preview", "/web", "shop/", "shop//web", "",
	} {
		rendered, code := releases(t, root, "", scope, "promote", "production", "ocel/shop/web:one")
		if code != 2 {
			t.Errorf("promote as %q exited %d, want a refusal", scope, code)
		}
		if rendered != "" {
			t.Errorf("promote as %q wrote %q to stdout", scope, rendered)
		}
	}
	held, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, entry := range held {
		if entry.IsDir() {
			t.Errorf("a refused scope wrote the window %q, and the gate is what stops a name reaching the filesystem at all", entry.Name())
		}
	}
}

func TestTheHelperRefusesACoordinateItCannotName(t *testing.T) {
	t.Parallel()

	root := releasesDir(t)
	for _, ref := range []string{"", "-rf", "ocel/shop/web:one; docker rmi $(docker images -q)", "ocel/shop/web:one\nocel/shop/web:two"} {
		if _, code := releases(t, root, "", "shop/web", "promote", "production", ref); code != 2 {
			t.Errorf("promote %q exited %d, want a refusal", ref, code)
		}
	}
}

func TestOneAppsWindowsAreReadTogetherAndAnothersAreNeverRead(t *testing.T) {
	root := releasesDir(t)
	promote(t, root, "shop/web", "production", "ocel/shop/web:live")
	for _, ref := range []string{"ocel/shop/web:p1", "ocel/shop/web:p2", "ocel/shop/web:p3"} {
		promote(t, root, "shop/web", "preview", ref)
	}
	promote(t, root, "shop/api", "production", "ocel/shop/api:live")

	dock := fakeDocker(t, nil, []string{"ocel/shop/web:live", "ocel/shop/web:p1", "ocel/shop/web:p2", "ocel/shop/web:p3", "ocel/shop/web:gone"})
	rendered, code := dock.reconcile(t, root, "shop/web", "ocel/shop/web")
	if code != 0 {
		t.Fatalf("reconcile exited %d", code)
	}
	if swept(rendered) != "ocel/shop/web:gone" {
		t.Errorf("reconcile removed %q, want ocel/shop/web:gone alone: preview churn cannot evict what production still names", swept(rendered))
	}
	if said := dock.listed(t); !strings.Contains(said, "reference=ocel/shop/web:*") {
		t.Errorf("the image listing ran as %q, and actual is one listing filtered to a single app", said)
	}
	if strings.Contains(dock.log(t), "ocel/shop/api") {
		t.Error("the sweep of web named api at all, and the filter and the desired set are computed over one scope")
	}
}

func TestARefOnlyARunningContainerNamesIsNeverRemoved(t *testing.T) {
	root := releasesDir(t)
	promote(t, root, "shop/web", "production", "ocel/shop/web:named")

	dock := fakeDocker(t, []string{"shop web ocel/shop/web:running"}, []string{"ocel/shop/web:named", "ocel/shop/web:running", "ocel/shop/web:orphan"})
	rendered, code := dock.reconcile(t, root, "shop/web", "ocel/shop/web")
	if code != 0 {
		t.Fatalf("reconcile exited %d", code)
	}
	if swept(rendered) != "ocel/shop/web:orphan" {
		t.Errorf("reconcile removed %q, want ocel/shop/web:orphan alone: a ref no window names is still held by the container serving it", swept(rendered))
	}
}

func TestAContainerCarryingTheAppLabelAndNoRefStopsTheSweep(t *testing.T) {
	root := releasesDir(t)
	promote(t, root, "shop/web", "production", "ocel/shop/web:named")

	dock := fakeDocker(t, []string{"shop web "}, []string{"ocel/shop/web:named", "ocel/shop/web:orphan"})
	rendered, code := dock.reconcile(t, root, "shop/web", "ocel/shop/web")
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
	promote(t, root, "shop/web", "production", "ocel/shop/web:named")

	dock := fakeDocker(t, nil, []string{"ocel/shop/web:named", "ocel/shop/web:orphan"})
	if _, code := dock.reconcile(t, root, "shop/web", "ocel/shop/web"); code != 0 {
		t.Fatal("the first reconcile refused")
	}
	rendered, code := dock.reconcile(t, root, "shop/web", "ocel/shop/web")
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
	dock := fakeDocker(t, nil, []string{"ocel/shop/web:held"})
	dock.refuse(t)

	rendered, code := dock.reconcile(t, root, "shop/web", "ocel/shop/web")
	if code != 0 {
		t.Fatalf("reconcile exited %d, want a daemon that refuses to leave the run successful", code)
	}
	if rendered != "" {
		t.Errorf("reconcile reported %q removed, and a daemon that refused removed nothing", rendered)
	}
	if !strings.Contains(dock.log(t), "rmi ocel/shop/web:held") {
		t.Error("reconcile never asked for the removal at all")
	}
}

func TestOneProjectsTeardownLeavesAnotherProjectsImagesOfTheSameAppStanding(t *testing.T) {
	root := releasesDir(t)
	promote(t, root, "shop/web", "production", "ocel/shop/web:live")
	promote(t, root, "blog/web", "production", "ocel/blog/web:live")
	promote(t, root, "blog/web", "production", "ocel/blog/web:next")

	if _, code := releases(t, root, "", "shop/web", "forget", "production"); code != 0 {
		t.Fatalf("forget exited %d", code)
	}
	dock := fakeDocker(t, nil, []string{"ocel/shop/web:live", "ocel/blog/web:live", "ocel/blog/web:next"})
	rendered, code := dock.reconcile(t, root, "shop/web", "ocel/shop/web")
	if code != 0 {
		t.Fatalf("reconcile exited %d", code)
	}
	if swept(rendered) != "ocel/shop/web:live" {
		t.Errorf("shop's teardown removed %q, want ocel/shop/web:live alone: two projects that each name an app web shared one repository and one window, and shop's sweep took blog's image out from under a load still importing it", swept(rendered))
	}
	if held := window(t, root, "blog/web", "production"); strings.Join(held, ",") != "ocel/blog/web:next,ocel/blog/web:live" {
		t.Errorf("blog's window reads %v after shop was torn down, want both refs it promoted", held)
	}
	if strings.Contains(dock.log(t), "rmi ocel/blog") {
		t.Errorf("shop's sweep asked the daemon to remove a blog image:\n%s", dock.log(t))
	}
}

func TestOneProjectsRunningContainerIsNeverReadAsAnothersUnderTheSameAppName(t *testing.T) {
	root := releasesDir(t)
	promote(t, root, "shop/web", "production", "ocel/shop/web:named")

	dock := fakeDocker(t, []string{"blog web ocel/shop/web:orphan"},
		[]string{"ocel/shop/web:named", "ocel/shop/web:orphan"})
	rendered, code := dock.reconcile(t, root, "shop/web", "ocel/shop/web")
	if code != 0 {
		t.Fatalf("reconcile exited %d", code)
	}
	if swept(rendered) != "ocel/shop/web:orphan" {
		t.Errorf("reconcile removed %q, want ocel/shop/web:orphan alone: the running set is read over both labels, and a container of another project named web keeps nothing of shop's alive", swept(rendered))
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
		"app=; project=; reference=\n" +
		"for arg in \"$@\"; do case $arg in\n" +
		"label=" + LabelApp + "=*) app=${arg#label=" + LabelApp + "=} ;;\n" +
		"label=" + LabelProject + "=*) project=${arg#label=" + LabelProject + "=} ;;\n" +
		"reference=*) reference=${arg#reference=} ;;\n" +
		"esac; done\n" +
		"case \"$1\" in\n" +
		"ps) awk -v p=\"$project\" -v a=\"$app\" '$1 == p && $2 == a { print substr($0, length($1) + length($2) + 3) }' " +
		filepath.Join(dir, "running") + " ;;\n" +
		"images) awk -v r=\"${reference%\\*}\" 'index($0, r) == 1' " + filepath.Join(dir, "images") + " ;;\n" +
		"rmi) if [ -f " + filepath.Join(dir, "refuse") + " ]; then echo 'image is in use' >&2; exit 1; fi\n" +
		"    grep -F -x -v -e \"$2\" " + filepath.Join(dir, "images") + " >" + filepath.Join(dir, "images.next") + " || true\n" +
		"    mv -f " + filepath.Join(dir, "images.next") + " " + filepath.Join(dir, "images") + " ;;\n" +
		"esac\n"
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	return dockerStub{dir: dir}
}

func (d dockerStub) reconcile(t *testing.T, root, scope, repository string) (string, int) {
	t.Helper()
	return releases(t, root, d.dir, scope, "reconcile", repository)
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
