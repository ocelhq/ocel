package host

import (
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"syscall"
	"testing"
	"time"
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
