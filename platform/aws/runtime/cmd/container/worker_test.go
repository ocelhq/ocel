package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"strings"
	"syscall"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/processenv"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

type consumed struct {
	worker   string
	url      string
	topology queues.Topology
}

func fakeConsumption(t *testing.T, then func(ctx context.Context, got consumed) error) chan consumed {
	t.Helper()
	starts := make(chan consumed, 1)
	was := consumeQueues
	consumeQueues = func(ctx context.Context, worker, url string, topology queues.Topology) error {
		got := consumed{worker: worker, url: url, topology: topology}
		starts <- got
		return then(ctx, got)
	}
	t.Cleanup(func() { consumeQueues = was })
	return starts
}

func workerEnv(t *testing.T) []string {
	t.Helper()
	topology, err := json.Marshal(queues.Topology{Table: "state", KeyPrefix: "p#", Workers: map[string]queues.Worker{"media": {Concurrency: 2}}})
	if err != nil {
		t.Fatal(err)
	}
	return []string{processenv.WorkerEnvVar + "=media", queues.EnvVar + "=" + string(topology)}
}

func TestAContainerRunningAWorkerPollsItsQueuesAndServesNothing(t *testing.T) {
	starts := fakeConsumption(t, func(ctx context.Context, got consumed) error {
		resp, err := http.Get(got.url + "/quit")
		if err == nil {
			resp.Body.Close()
		}
		<-ctx.Done()
		return nil
	})
	r := launch(t, "serve", workerEnv(t)...)

	var got consumed
	select {
	case got = <-starts:
	case <-time.After(30 * time.Second):
		t.Fatal("the worker never started polling")
	}
	if got.worker != "media" || got.topology.Workers["media"].Concurrency != 2 {
		t.Errorf("polling started for %q over %+v, want worker media and the topology the deploy pinned", got.worker, got.topology)
	}
	if !strings.HasPrefix(got.url, "http://127.0.0.1:") {
		t.Errorf("the worker was addressed at %q, want its loopback port: a worker takes no traffic from outside", got.url)
	}
	if code := r.await(t); code != 0 {
		t.Errorf("run = %d, want 0 once the worker entry exited cleanly", code)
	}
	if conn, err := net.DialTimeout("tcp", "127.0.0.1:"+r.port, 200*time.Millisecond); err == nil {
		conn.Close()
		t.Error("the runtime listened on the exposed port, so a load balancer could send a worker traffic")
	}
}

func TestAWorkerThatReceivesTermStopsPollingBeforeItsEntryIsToldToStop(t *testing.T) {
	stopped := make(chan struct{})
	starts := fakeConsumption(t, func(ctx context.Context, _ consumed) error {
		<-ctx.Done()
		close(stopped)
		return nil
	})
	r := launch(t, "serve", workerEnv(t)...)
	select {
	case <-starts:
	case <-time.After(30 * time.Second):
		t.Fatal("the worker never started polling")
	}

	if err := syscall.Kill(syscall.Getpid(), syscall.SIGTERM); err != nil {
		t.Fatal(err)
	}

	select {
	case <-stopped:
	case <-time.After(30 * time.Second):
		t.Fatal("polling never stopped after SIGTERM, so a task being replaced would keep taking messages it cannot finish")
	}
	if code := r.await(t); code != 0 {
		t.Errorf("run = %d, want 0 for a worker that stopped on request", code)
	}
}

func TestAWorkerWhoseEntryExitsExitsWithItsCode(t *testing.T) {
	fakeConsumption(t, func(ctx context.Context, _ consumed) error {
		<-ctx.Done()
		return nil
	})
	r := launch(t, "exit:3", workerEnv(t)...)

	if code := r.await(t); code != 3 {
		t.Errorf("run = %d, want 3, the worker entry's own code", code)
	}
}

func TestAWorkerContainerWithoutAQueueTopologyIsRefusedAtStart(t *testing.T) {
	fakeConsumption(t, func(context.Context, consumed) error { return nil })
	r := launch(t, "serve", processenv.WorkerEnvVar+"=media")

	if code := r.await(t); code != 1 {
		t.Errorf("run = %d, want 1: a worker with no queues to serve is a deploy that went wrong", code)
	}
}
