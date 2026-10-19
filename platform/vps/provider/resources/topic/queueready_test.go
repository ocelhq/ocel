//go:build integration

package topic

import (
	"os"
	"os/exec"
	"strconv"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/enginetest"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

func TestMain(m *testing.M) { os.Exit(enginetest.Main(m)) }

func TestTheQueueDatabaseIsNotReadyWhileItsImageIsStillInitialisingIt(t *testing.T) {
	stack, err := naming.ParseStackName("prod--infra")
	if err != nil {
		t.Fatal(err)
	}
	spec, err := NewQueueContainer(provider.StackRef{Project: "shop", Tier: environment.TierProduction, Name: stack})
	if err != nil {
		t.Fatal(err)
	}
	name := "ocel-queue-ready-" + strconv.Itoa(os.Getpid())

	argv := append([]string{"run", "--detach", "--name", name, "--network", "none",
		"--tmpfs", "/docker-entrypoint-initdb.d:mode=1777"}, enginetest.RunLabelArgs(t)...)
	argv = append(argv, "--env", "POSTGRES_PASSWORD=probe-secret", "--env", "POSTGRES_DB="+boxapi.QueueDatabaseName,
		"--entrypoint", "sh", spec.Image,
		"-c", `printf 'sleep 15\n' > /docker-entrypoint-initdb.d/hold.sh && exec docker-entrypoint.sh "$@"`, "sh")
	argv = append(argv, spec.Args...)
	if said, err := exec.Command("docker", argv...).CombinedOutput(); err != nil {
		t.Fatalf("start the queue database: %v\n%s", err, said)
	}
	t.Cleanup(func() { _ = exec.Command("docker", "rm", "--force", name).Run() })

	deadline := time.Now().Add(90 * time.Second)
	for exec.Command("docker", "exec", name, "pg_isready", "--username", "postgres", "--quiet").Run() != nil {
		if time.Now().After(deadline) {
			logged, _ := exec.Command("docker", "logs", "--tail", "20", name).CombinedOutput()
			t.Fatalf("the image's own initialising server never answered over the socket:\n%s", logged)
		}
		time.Sleep(time.Second)
	}

	if said, err := exec.Command("docker", append([]string{"exec", name}, spec.Ready...)...).CombinedOutput(); err == nil {
		t.Errorf("%v answered %q while the image's entrypoint still held its initialising server, which it stops before it starts the one a deploy talks to, so the credential written next meets a server shutting down", spec.Ready, said)
	}

	deadline = time.Now().Add(90 * time.Second)
	for exec.Command("docker", append([]string{"exec", name}, spec.Ready...)...).Run() != nil {
		if time.Now().After(deadline) {
			logged, _ := exec.Command("docker", "logs", "--tail", "20", name).CombinedOutput()
			t.Fatalf("%v never answered once the image had started the server a deploy talks to:\n%s", spec.Ready, logged)
		}
		time.Sleep(time.Second)
	}
}
