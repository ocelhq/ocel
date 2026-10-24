//go:build integration

package queues_test

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/enginetest"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/images"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/vps/box/queues"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

func TestMain(m *testing.M) { os.Exit(enginetest.Main(m)) }

type boundSockets struct {
	container string
	dir       string
}

func (b boundSockets) MountSource(_ context.Context, container, destination string) (string, error) {
	if container != b.container || destination != boxapi.QueueSocketsPath {
		return "", errors.New(container + " mounts nothing at " + destination)
	}
	return b.dir, nil
}

type probeSecret struct{}

func (probeSecret) Open(context.Context, environment.Tier, seal.AssociatedData, []byte) ([]byte, error) {
	return []byte("probe-secret"), nil
}

func socketOnlyQueueDatabase(t *testing.T) boundSockets {
	t.Helper()
	sockets := boundSockets{container: "ocel-queue-sockets-" + strconv.Itoa(os.Getpid()), dir: enginetest.BindSource(t)}
	if err := os.Chmod(sockets.dir, 0o777); err != nil {
		t.Fatal(err)
	}
	argv := append([]string{"run", "--detach", "--name", sockets.container}, enginetest.RunLabelArgs(t)...)
	argv = append(argv,
		"--env", "POSTGRES_PASSWORD=probe-secret", "--env", "POSTGRES_DB="+boxapi.QueueDatabaseName,
		"--mount", "type=bind,src="+sockets.dir+",dst="+boxapi.QueueSocketsPath,
		images.QueueDatabase(), "postgres", "-c", "listen_addresses=")
	if said, err := exec.Command("docker", argv...).CombinedOutput(); err != nil {
		t.Fatalf("start the queue database: %v\n%s", err, said)
	}
	t.Cleanup(func() {
		_ = exec.Command("docker", "stop", sockets.container).Run()
		_ = exec.Command("docker", "rm", "--force", sockets.container).Run()
	})
	deadline := time.Now().Add(90 * time.Second)
	for answered := 0; answered < 2; {
		said, err := exec.Command("docker", "exec", sockets.container, "pg_isready", "--username", "postgres").CombinedOutput()
		switch {
		case err == nil:
			answered++
		case time.Now().After(deadline):
			logged, _ := exec.Command("docker", "logs", "--tail", "20", sockets.container).CombinedOutput()
			t.Fatalf("the queue database never took a connection over its socket: %v\n%s\n%s", err, said, logged)
		default:
			answered = 0
		}
		time.Sleep(time.Second)
	}
	granting := exec.Command("docker", "exec", "--interactive", sockets.container, "psql", "--username", "postgres", "-v", "ON_ERROR_STOP=1")
	granting.Stdin = strings.NewReader(boxapi.QueueRoleSQL())
	if said, err := granting.CombinedOutput(); err != nil {
		t.Fatalf("grant the queue role: %v\n%s", err, said)
	}
	return sockets
}

func TestARecordedTaskIsTriggeredAndReadBackThroughTheEngineAsTheQueueRoleOverTheSocket(t *testing.T) {
	sockets := socketOnlyQueueDatabase(t)

	store := fake.NewKeyValues()
	record(t, store, boxapi.QueueDatabaseKey(tier, "shop", "prod"), boxapi.QueueDatabase{Container: sockets.container, Stack: "prod--infra"})
	record(t, store, boxapi.QueueTopicKey(tier, "shop", "prod", "send-email"),
		[]byte(`{"consumers":[{"name":"send-email","worker":"worker","exclusive":true}]}`))
	engines := &queues.Engines{
		Records:     store,
		Tiers:       []environment.Tier{tier},
		Cipher:      probeSecret{},
		Mounts:      sockets,
		Open:        queues.OpenPgmq,
		IsAnswering: queues.IsWorkerAnswering,
	}
	ctx, stop := context.WithCancel(context.Background())
	defer stop()
	if err := engines.Reconcile(ctx); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}
	t.Cleanup(func() {
		stop()
		engines.Close()
	})

	tasks, _, served := engines.Served(tier, "shop", "prod")
	if !served {
		t.Fatal("the recorded queue is not served")
	}
	triggered, err := tasks.Trigger(ctx, &taskv1.TriggerRequest{Task: "send-email", Payload: []byte(`{"to":"ada"}`)})
	if err != nil {
		t.Fatalf("Trigger() = %v", err)
	}
	read, err := tasks.RetrieveRun(ctx, &taskv1.RetrieveRunRequest{Id: triggered.GetId()})
	if err != nil {
		t.Fatalf("RetrieveRun() = %v", err)
	}
	if read.GetRun().GetTask() != "send-email" || string(read.GetRun().GetPayload()) != `{"to":"ada"}` {
		t.Errorf("RetrieveRun() = %v, want the send-email run with the payload it was triggered with", read.GetRun())
	}
}
