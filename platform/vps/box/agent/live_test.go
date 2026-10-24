//go:build integration

package agent

import (
	"context"
	"math/rand/v2"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/enginetest"
	"github.com/ocelhq/ocel/pkg/variablestore"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

func TestMain(m *testing.M) { os.Exit(enginetest.Main(m)) }

func containerRuntime(t *testing.T, dir string) string {
	t.Helper()
	binary := filepath.Join(dir, "runtime")
	build := exec.Command("go", "build", "-trimpath", "-o", binary, "../cmd/container")
	build.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux")
	if said, err := build.CombinedOutput(); err != nil {
		t.Fatalf("build the container runtime: %v\n%s", err, said)
	}
	return binary
}

const testImage = "mirror.gcr.io/library/alpine:3.21"

func pulled(t *testing.T, image string) {
	t.Helper()
	if exec.Command("docker", "image", "inspect", image).Run() == nil {
		return
	}
	var said []byte
	var err error
	for attempt := range 6 {
		if said, err = exec.Command("docker", "pull", "--quiet", image).CombinedOutput(); err == nil {
			return
		}
		if !strings.Contains(strings.ToLower(string(said)), "toomanyrequests") {
			break
		}
		time.Sleep(time.Second<<attempt + rand.N(time.Second))
	}
	t.Fatalf("pull %s: %v\n%s", image, err, said)
}

func TestAContainerReadsItsSecretOffTheBoxThroughTheRuntimeAndTheAgent(t *testing.T) {
	dir := enginetest.BindSource(t)
	pulled(t, testImage)
	runtimeBinary := containerRuntime(t, dir)

	b := aBox(t, dir)
	b.set(t, shop, variablestore.Coordinate{Cell: variablestore.Cell{Key: "DATABASE_URL"}}, "postgres://app:hunter2@db.internal/orders")
	b.dump(t)
	inspect, err := NewDocker()
	if err != nil {
		t.Fatal(err)
	}
	socketDir := filepath.Join(dir, "run")
	if err := os.MkdirAll(socketDir, 0o755); err != nil {
		t.Fatal(err)
	}
	ln, err := net.Listen("unix", filepath.Join(socketDir, boxapi.SocketFile))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(socketDir, boxapi.SocketFile), 0o666); err != nil {
		t.Fatal(err)
	}
	ctx, stop := context.WithCancel(context.Background())
	served := make(chan error, 1)
	go func() { served <- (&Server{Inspect: inspect, Resolve: b.resolver()}).Serve(ctx, ln) }()
	t.Cleanup(func() {
		stop()
		if err := <-served; err != nil {
			t.Errorf("Serve() = %v", err)
		}
	})

	manifest := manifestFor(t, "shop", "")
	argv := append([]string{"run", "--rm", "--pull", "never", "--network", "none"}, enginetest.RunLabelArgs(t)...)
	run := exec.Command("docker", append(argv,
		"--mount", "type=bind,src="+socketDir+",dst="+boxapi.SocketDir+",readonly",
		"--volume", runtimeBinary+":"+containerimage.RuntimePath+":ro",
		"--env", boxapi.EnvVar+"="+manifest,
		"--env", containerimage.PortEnvVar+"="+containerimage.PortText,
		testImage, containerimage.RuntimePath,
		"sh", "-c", `cat "$OCEL_LIVE_DIR/DATABASE_URL"; echo; echo "keys=$OCEL_LIVE_KEYS"`)...)
	run.WaitDelay = 2 * time.Minute
	said, err := run.CombinedOutput()
	if err != nil {
		t.Fatalf("the container did not run behind the runtime: %v\n%s", err, said)
	}
	if !strings.Contains(string(said), "postgres://app:hunter2@db.internal/orders") {
		t.Errorf("the app read %q off its live directory, want the secret sealed on the box", said)
	}
	if !strings.Contains(string(said), "keys=DATABASE_URL") {
		t.Errorf("the app was told %q, want the live keys named", said)
	}

}
