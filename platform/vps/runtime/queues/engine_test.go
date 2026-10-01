package queues_test

import (
	"context"
	"encoding/base64"
	"os"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/provider/enginetest"
	"github.com/ocelhq/ocel/pkg/provider/fake"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
	"github.com/ocelhq/ocel/platform/vps/runtime/agent"
	"github.com/ocelhq/ocel/platform/vps/runtime/queues"
)

func TestMain(m *testing.M) { os.Exit(enginetest.Main(m)) }

type probeSecret struct{}

func (probeSecret) Open(context.Context, environment.Tier, seal.AssociatedData, []byte) ([]byte, error) {
	return []byte("probe-secret"), nil
}

func TestARecordedTaskIsTriggeredAndReadBackThroughTheEngineOnItsQueueDatabase(t *testing.T) {
	server := enginetest.SharedQueueDatabase(t)
	server.Claim(t, live.QueueResource)
	docker, err := agent.NewDocker()
	if err != nil {
		t.Fatal(err)
	}

	store := fake.NewKeyValues()
	record(t, store, live.QueueDatabaseKey(tier, "shop", "prod"), live.QueueDatabase{
		Container: server.Name, Stack: "prod--infra", Sealed: base64.StdEncoding.EncodeToString([]byte("sealed")),
	})
	record(t, store, live.QueueTopicKey(tier, "shop", "prod", "send-email"),
		[]byte(`{"consumers":[{"name":"send-email","worker":"worker","exclusive":true}]}`))
	engines := &queues.Engines{
		Records:   store,
		Tiers:     []environment.Tier{tier},
		Cipher:    probeSecret{},
		Addresses: docker,
		Open:      queues.OpenPgmq,
		Answers:   queues.WorkerAnswers,
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
