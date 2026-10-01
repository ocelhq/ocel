package main

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"strings"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/processenv"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/task/v1/taskv1connect"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	variables "github.com/ocelhq/ocel/platform/vps/provider/live"
)

type agentTasks struct {
	taskv1connect.UnimplementedTaskServiceHandler
	triggered []string
}

func (a *agentTasks) Trigger(_ context.Context, req *taskv1.TriggerRequest) (*taskv1.TriggerResponse, error) {
	a.triggered = append(a.triggered, req.GetTask())
	return &taskv1.TriggerResponse{Id: "01JABCDEFGHJKMNPQRSTVWXYZ0"}, nil
}

func agentServing(t *testing.T, values map[string]string, tasks *agentTasks) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "values.sock")
	ln, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	mux.HandleFunc(variables.ValuesPath, func(w http.ResponseWriter, _ *http.Request) {
		_ = json.NewEncoder(w).Encode(variables.Answer{Values: values})
	})
	mux.Handle(taskv1connect.NewTaskServiceHandler(tasks))
	server := &http.Server{Handler: mux}
	go func() { _ = server.Serve(ln) }()
	t.Cleanup(func() { _ = server.Close() })
	return socket
}

type withToken string

func (w withToken) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Set("Authorization", localrpc.FormatAuthHeader(string(w)))
	return http.DefaultTransport.RoundTrip(req)
}

func TestTheRuntimeFrontsTheBoxsQueueForAnAppThatBindsATask(t *testing.T) {
	manifest := variables.Manifest{
		Slug: "shop", Tier: "production", Queue: "prod",
		Bindings: []live.Binding{{Name: "task--send-email", Key: "OCEL_RESOURCE_TASK_send-email", Type: bindingsv1.BindingType_BINDING_TYPE_TASK}},
	}
	rendered, err := variables.Render(manifest)
	if err != nil {
		t.Fatal(err)
	}
	tasks := &agentTasks{}
	socket := agentServing(t, map[string]string{"OCEL_RESOURCE_TASK_send-email": `{"name":"task--send-email","task":{}}`}, tasks)
	values, err := resolve(context.Background(), string(rendered), socket, filepath.Join(t.TempDir(), "live"))
	if err != nil {
		t.Fatalf("resolve() = %v", err)
	}

	served, err := proxying(manifest, values, socket, "127.0.0.1:1")
	if err != nil {
		t.Fatalf("proxying() = %v", err)
	}
	t.Cleanup(func() { _ = served.Close() })
	var address, token string
	for _, entry := range served.Env {
		if value, found := strings.CutPrefix(entry, processenv.RuntimeAddressEnvVar+"="); found {
			address = value
		}
		if value, found := strings.CutPrefix(entry, localrpc.SessionTokenEnvVar+"="); found {
			token = value
		}
	}
	if address == "" || token == "" {
		t.Fatalf("the app is handed %q, which names no runtime to trigger through", served.Env)
	}

	_, err = taskv1connect.NewTaskServiceClient(http.DefaultClient, address).Trigger(context.Background(), &taskv1.TriggerRequest{Task: "send-email", Payload: []byte(`{}`)})
	if code := connect.CodeOf(err); code != connect.CodeUnauthenticated || len(tasks.triggered) != 0 {
		t.Errorf("a trigger without the app's token = %v reaching %v, want it refused before the box", err, tasks.triggered)
	}
	triggered, err := taskv1connect.NewTaskServiceClient(&http.Client{Transport: withToken(token)}, address).
		Trigger(context.Background(), &taskv1.TriggerRequest{Task: "send-email", Payload: []byte(`{}`)})
	if err != nil {
		t.Fatalf("Trigger() with the app's token = %v", err)
	}
	if triggered.GetId() == "" || len(tasks.triggered) != 1 || tasks.triggered[0] != "send-email" {
		t.Errorf("Trigger() = %v reaching %v, want the box's agent to take send-email", triggered, tasks.triggered)
	}
}

func TestAWorkerContainerRunsTheWorkerEntryItsImageCarriesInsteadOfTheApp(t *testing.T) {
	image := []string{"pnpm", "start"}
	nodeEntry := func(path string) bool { return path == "/ocel/worker/worker.mjs" }
	if got := commandFor([]string{processenv.WorkerEnvVar + "=ledger"}, image, nodeEntry); strings.Join(got, " ") != "node /ocel/worker/worker.mjs" {
		t.Errorf("a worker container runs %q, want the node worker entry", got)
	}
	if got := commandFor([]string{"PORT=8080"}, image, nodeEntry); strings.Join(got, " ") != "pnpm start" {
		t.Errorf("the app's own container runs %q, want the image's command", got)
	}
}
