package host

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestAReconcileWaitsForTheLockAPromoteOfTheSameAppHoldsAndForNoOtherApps(t *testing.T) {
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

	dock := fakeDocker(t, nil, []string{"ocel/shop/web:one", "ocel/shop/web:gone", "ocel/shop/api:gone"})
	web := make(chan int, 1)
	go func() {
		_, code := dock.reconcile(t, root, "shop/web", "ocel/shop/web")
		web <- code
	}()
	api := make(chan int, 1)
	go func() {
		_, code := dock.reconcile(t, root, "shop/api", "ocel/shop/api")
		api <- code
	}()
	select {
	case code := <-api:
		if code != 0 {
			t.Fatalf("api's reconcile exited %d", code)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("api's reconcile was still waiting while web's promote held web's lock: the lock is per app")
	}
	select {
	case <-web:
		t.Fatal("web's reconcile finished while web's promote was mid-write: the ref that promote is naming is in no window the sweep read, so an rmi of it is exactly the race the lock closes")
	case <-time.After(time.Second):
	}
	if err := slow.Wait(); err != nil {
		t.Fatalf("web's promote exited: %v", err)
	}
	select {
	case code := <-web:
		if code != 0 {
			t.Fatalf("web's reconcile exited %d after the promote released the lock", code)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("web's reconcile never finished after the promote released its lock")
	}
	if refs := window(t, root, "shop/web", "production"); !slices.Equal(refs, []string{"ocel/shop/web:one"}) {
		t.Errorf("the window has %v after the promote landed, want the one ref it wrote", refs)
	}
	if log := dock.log(t); !strings.Contains(log, "rmi ocel/shop/web:gone") || strings.Contains(log, "rmi ocel/shop/web:one") {
		t.Errorf("the sweep ran\n%s\nwant ocel/shop/web:gone removed and the ref the promote landed kept: a reconcile that waits for the lock reads the window the promote wrote", log)
	}
}

func TestAReconcileWaitingOnAScopeAForgetRemovedWaitsForThePromoteThatMadeItAgain(t *testing.T) {
	root := releasesDir(t)
	removed := filepath.Join(root, "shop", "web")
	if err := os.MkdirAll(removed, 0o755); err != nil {
		t.Fatal(err)
	}
	held, err := os.Open(removed)
	if err != nil {
		t.Fatal(err)
	}
	defer held.Close()
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_EX); err != nil {
		t.Fatal(err)
	}

	dock := fakeDocker(t, nil, []string{"ocel/shop/web:one", "ocel/shop/web:gone"})
	web := make(chan int, 1)
	go func() {
		_, code := dock.reconcile(t, root, "shop/web", "ocel/shop/web")
		web <- code
	}()
	awaitFlockWaiter(t, removed)
	if err := os.Remove(removed); err != nil {
		t.Fatal(err)
	}

	script := filepath.Join(t.TempDir(), "releases")
	if err := os.WriteFile(script, releasesScript, 0o755); err != nil {
		t.Fatal(err)
	}
	slow := exec.Command("/bin/sh", script, "shop/web", "promote", "production", "ocel/shop/web:one")
	slow.Env = append(os.Environ(), "OCEL_RELEASES_ROOT="+root, "PATH="+slowGrep(t)+":"+os.Getenv("PATH"))
	if err := slow.Start(); err != nil {
		t.Fatal(err)
	}
	for range 200 {
		if len(staging(t, root)) > 0 {
			break
		}
		time.Sleep(10 * time.Millisecond)
	}
	if len(staging(t, root)) == 0 {
		t.Fatal("the promote never reached its write, so it is holding no lock and this test proves nothing")
	}
	if err := syscall.Flock(int(held.Fd()), syscall.LOCK_UN); err != nil {
		t.Fatal(err)
	}

	select {
	case <-web:
		t.Fatal("the reconcile finished while the promote was mid-write: it ran under the lock of the scope the forget removed, not the one the promote is writing")
	case <-time.After(time.Second):
	}
	if err := slow.Wait(); err != nil {
		t.Fatalf("the promote exited: %v", err)
	}
	if code := <-web; code != 0 {
		t.Fatalf("the reconcile exited %d", code)
	}
	if log := dock.log(t); strings.Contains(log, "rmi ocel/shop/web:one") || !strings.Contains(log, "rmi ocel/shop/web:gone") {
		t.Errorf("the sweep ran\n%s\nwant the promoted ref kept and ocel/shop/web:gone removed", log)
	}
}

func awaitFlockWaiter(t *testing.T, path string) {
	t.Helper()
	var st unix.Stat_t
	if err := unix.Stat(path, &st); err != nil {
		t.Fatal(err)
	}
	file := fmt.Sprintf("%02x:%02x:%d", unix.Major(st.Dev), unix.Minor(st.Dev), st.Ino)
	for range 500 {
		locks, err := os.ReadFile("/proc/locks")
		if err != nil {
			t.Fatal(err)
		}
		for line := range strings.Lines(string(locks)) {
			fields := strings.Fields(line)
			if len(fields) > 6 && fields[1] == "->" && fields[2] == "FLOCK" && fields[6] == file {
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}
	t.Fatalf("nothing ever waited on the flock of %s", path)
}
