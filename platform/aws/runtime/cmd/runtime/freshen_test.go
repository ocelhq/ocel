package main

import (
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	"github.com/ocelhq/ocel/platform/aws/runtime/tasks"
)

func TestAnExecutablesInvocationFreshensTheLiveValuesItsChildReadsBeforeItIsForwarded(t *testing.T) {
	command := httpChildCommand(t)
	child, err := startExecutable(command, freePort(t), append(helperEnv(), helperSaidVar+"=what it was handed"), 20*time.Second)
	if err != nil {
		t.Fatalf("startExecutable: %v", err)
	}
	values := &stubValues{}
	child.live = values

	rt, _ := fakeRuntime(t, []byte(getEvent))
	if err := handleInvocation(t.Context(), rt, child); err != nil {
		t.Fatalf("handleInvocation: %v", err)
	}

	if got := values.freshened(); got != 1 {
		t.Errorf("the live values were freshened %d times for one invocation, want once: an executable reads them from the projection, and a token minted at start expires there after 15 minutes", got)
	}
}

func TestAWorkersInvocationFreshensTheLiveValuesItsEntryReadsBeforeItIsAnswered(t *testing.T) {
	values := &stubValues{}
	engine := tasks.New(tasks.Config{Topology: queues.Topology{Topics: map[string]queues.Topic{}}})
	rt, _ := fakeRuntime(t, []byte(`{"ocel":{"warm":1}}`))
	inv, err := rt.next(t.Context())
	if err != nil {
		t.Fatalf("next: %v", err)
	}

	answerWorkerInvocation(t.Context(), rt, inv, engine, values)

	if got := values.freshened(); got != 1 {
		t.Errorf("the live values were freshened %d times for one invocation, want once: a worker reads them from the projection, and a token minted at start expires there after 15 minutes", got)
	}
}
