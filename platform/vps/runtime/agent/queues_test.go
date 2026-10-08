package agent

import (
	"context"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"sync"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/images"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/task/v1/taskv1connect"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/topic/v1/topicv1connect"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	variables "github.com/ocelhq/ocel/platform/vps/provider/live"
)

type queueTasks struct {
	taskv1connect.UnimplementedTaskServiceHandler
	queue     string
	mu        sync.Mutex
	triggered []string
}

func (q *queueTasks) Trigger(_ context.Context, req *taskv1.TriggerRequest) (*taskv1.TriggerResponse, error) {
	q.mu.Lock()
	defer q.mu.Unlock()
	q.triggered = append(q.triggered, req.GetTask())
	return &taskv1.TriggerResponse{Id: q.queue + "-run"}, nil
}

type queueTopics struct {
	topicv1connect.UnimplementedTopicServiceHandler
	queue string
}

func (q *queueTopics) Send(context.Context, *topicv1.SendRequest) (*topicv1.SendResponse, error) {
	return &topicv1.SendResponse{MessageId: q.queue + "-message"}, nil
}

type queueKey struct {
	tier    environment.Tier
	project string
	env     string
}

type servedQueues map[queueKey]*queueTasks

func (s servedQueues) Served(tier environment.Tier, project, env string) (taskv1connect.TaskServiceHandler, topicv1connect.TopicServiceHandler, bool) {
	tasks, found := s[queueKey{tier, project, env}]
	if !found {
		return nil, nil, false
	}
	return tasks, &queueTopics{queue: tasks.queue}, true
}

func queueManifest(t *testing.T, slug, queue string) string {
	t.Helper()
	rendered, err := variables.Render(variables.Manifest{
		Slug: slug, Tier: "production", Queue: queue,
		Bindings: []live.Binding{{Name: "task--send-email", Key: "OCEL_RESOURCE_TASK_send-email"}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return string(rendered)
}

func overSocket(socket string) *http.Client {
	return &http.Client{Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
		return (&net.Dialer{}).DialContext(ctx, "unix", socket)
	}}}
}

func TestACallerReachesTheQueueItsOwnManifestNamesAndNoOther(t *testing.T) {
	t.Parallel()
	shop := &queueTasks{queue: "shop-prod"}
	queues := servedQueues{
		{environment.TierProduction, "shop", "prod"}: shop,
		{environment.TierProduction, "blog", "prod"}: {queue: "blog-prod"},
	}
	socket := serving(t, &Server{
		Proc:    procNaming(t, "0::/docker/"+containerID+"\n"),
		Inspect: &inspecting{manifests: map[string]string{containerID: queueManifest(t, "shop", "prod")}},
		Resolve: &resolving{},
		Queues:  queues,
	})

	tasks := taskv1connect.NewTaskServiceClient(overSocket(socket), "http://ocel-live")
	triggered, err := tasks.Trigger(context.Background(), &taskv1.TriggerRequest{Task: "send-email"})
	if err != nil {
		t.Fatalf("Trigger() = %v", err)
	}
	if triggered.GetId() != "shop-prod-run" || len(shop.triggered) != 1 {
		t.Errorf("Trigger() = %q reaching %v, want shop's own queue", triggered.GetId(), shop.triggered)
	}
	topics := topicv1connect.NewTopicServiceClient(overSocket(socket), "http://ocel-live")
	sent, err := topics.Send(context.Background(), &topicv1.SendRequest{Topic: "orders"})
	if err != nil || sent.GetMessageId() != "shop-prod-message" {
		t.Errorf("Send() = %v, %v, want it sent through shop's own queue", sent, err)
	}
}

func TestADirectCallFromOutsideEveryContainerIsRefusedBeforeAnyQueueIsReached(t *testing.T) {
	t.Parallel()
	shop := &queueTasks{queue: "shop-prod"}
	inspect := &inspecting{manifests: map[string]string{containerID: queueManifest(t, "shop", "prod")}}
	socket := serving(t, &Server{
		Proc:    procNaming(t, "0::/user.slice/user-1000.slice/session-3.scope\n"),
		Inspect: inspect,
		Resolve: &resolving{},
		Queues:  servedQueues{{environment.TierProduction, "shop", "prod"}: shop},
	})

	_, err := taskv1connect.NewTaskServiceClient(overSocket(socket), "http://ocel-live").
		Trigger(context.Background(), &taskv1.TriggerRequest{Task: "send-email"})
	if code := connect.CodeOf(err); code != connect.CodePermissionDenied {
		t.Errorf("an unauthenticated Trigger() = %v (%s), want %s", err, code, connect.CodePermissionDenied)
	}
	if len(shop.triggered) != 0 || len(inspect.asked) != 0 {
		t.Errorf("an unidentified caller reached the queue (%v) or the engine (%v)", shop.triggered, inspect.asked)
	}
}

func TestAProcessInsideTheRightContainerOtherThanItsRuntimeIsRefusedByEveryMethod(t *testing.T) {
	t.Parallel()
	shop := &queueTasks{queue: "shop-prod"}
	socket := serving(t, &Server{
		Proc: procNaming(t, "0::/docker/"+containerID+"\n"),
		Inspect: &inspecting{
			manifests: map[string]string{containerID: queueManifest(t, "shop", "prod")},
			initPID:   os.Getpid() + 1,
		},
		Resolve: &resolving{},
		Queues:  servedQueues{{environment.TierProduction, "shop", "prod"}: shop},
	})
	tasks := taskv1connect.NewTaskServiceClient(overSocket(socket), "http://ocel-live")
	topics := topicv1connect.NewTopicServiceClient(overSocket(socket), "http://ocel-live")
	ctx := context.Background()
	calls := map[string]error{}
	_, calls["Trigger"] = tasks.Trigger(ctx, &taskv1.TriggerRequest{Task: "send-email"})
	_, calls["BatchTrigger"] = tasks.BatchTrigger(ctx, &taskv1.BatchTriggerRequest{})
	_, calls["RetrieveRun"] = tasks.RetrieveRun(ctx, &taskv1.RetrieveRunRequest{})
	_, calls["ListRuns"] = tasks.ListRuns(ctx, &taskv1.ListRunsRequest{})
	_, calls["CancelRun"] = tasks.CancelRun(ctx, &taskv1.CancelRunRequest{})
	_, calls["ReplayRun"] = tasks.ReplayRun(ctx, &taskv1.ReplayRunRequest{})
	_, calls["RescheduleRun"] = tasks.RescheduleRun(ctx, &taskv1.RescheduleRunRequest{})
	_, calls["Send"] = topics.Send(ctx, &topicv1.SendRequest{Topic: "orders"})
	_, calls["ListDeadLetters"] = topics.ListDeadLetters(ctx, &topicv1.ListDeadLettersRequest{})
	_, calls["RedriveDeadLetters"] = topics.RedriveDeadLetters(ctx, &topicv1.RedriveDeadLettersRequest{})
	_, calls["PurgeDeadLetters"] = topics.PurgeDeadLetters(ctx, &topicv1.PurgeDeadLettersRequest{})
	_, calls["CountDeadLetters"] = topics.CountDeadLetters(ctx, &topicv1.CountDeadLettersRequest{})
	for method, err := range calls {
		if code := connect.CodeOf(err); code != connect.CodePermissionDenied {
			t.Errorf("%s() from a process the app started = %v (%s), want %s", method, err, code, connect.CodePermissionDenied)
		}
	}
	if len(shop.triggered) != 0 {
		t.Errorf("a process other than the container's runtime reached the queue: %v", shop.triggered)
	}
}

func TestTheDaemonNamesTheProcessAContainerRunsAsItsInit(t *testing.T) {
	t.Parallel()
	docker := dockerAnswering(t, map[string]string{
		"/containers/" + containerID + "/json": `{"State":{"Running":true,"Pid":4242},"Config":{"Env":["PATH=/usr/bin","` + variables.EnvVar + `={\"slug\":\"shop\"}"]}}`,
		"/containers/stopped/json":             `{"State":{"Running":false,"Pid":0},"Config":{"Env":[]}}`,
	})
	running, err := docker.ReadContainer(context.Background(), containerID)
	if err != nil || running.InitPID != 4242 || running.Manifest != `{"slug":"shop"}` {
		t.Errorf("ReadContainer(%s) = %+v, %v, want init 4242 and its manifest", containerID, running, err)
	}
	stopped, err := docker.ReadContainer(context.Background(), "stopped")
	if err != nil || stopped.InitPID != 0 {
		t.Errorf("ReadContainer(stopped) = %+v, %v, want no init process", stopped, err)
	}
}

func TestAContainerWhoseManifestNamesNoQueueIsToldItHasNone(t *testing.T) {
	t.Parallel()
	socket := serving(t, &Server{
		Proc:    procNaming(t, "0::/docker/"+containerID+"\n"),
		Inspect: &inspecting{manifests: map[string]string{containerID: queueManifest(t, "shop", "")}},
		Resolve: &resolving{},
		Queues:  servedQueues{{environment.TierProduction, "shop", "prod"}: {queue: "shop-prod"}},
	})
	_, err := taskv1connect.NewTaskServiceClient(overSocket(socket), "http://ocel-live").
		Trigger(context.Background(), &taskv1.TriggerRequest{Task: "send-email"})
	var refused *connect.Error
	if !errors.As(err, &refused) || refused.Code() != connect.CodeFailedPrecondition {
		t.Errorf("Trigger() = %v, want %s: this deployment declares no task or topic", err, connect.CodeFailedPrecondition)
	}
}

func TestAQueueTheBoxIsNotServingYetIsUnavailableNotMissing(t *testing.T) {
	t.Parallel()
	socket := serving(t, &Server{
		Proc:    procNaming(t, "0::/docker/"+containerID+"\n"),
		Inspect: &inspecting{manifests: map[string]string{containerID: queueManifest(t, "shop", "prod")}},
		Resolve: &resolving{},
		Queues:  servedQueues{},
	})
	_, err := taskv1connect.NewTaskServiceClient(overSocket(socket), "http://ocel-live").
		Trigger(context.Background(), &taskv1.TriggerRequest{Task: "send-email"})
	if code := connect.CodeOf(err); code != connect.CodeUnavailable {
		t.Errorf("Trigger() = %v (%s), want %s while the box opens the queue", err, code, connect.CodeUnavailable)
	}
}

func dockerAnswering(t *testing.T, answers map[string]string) *Docker {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		answer, found := answers[r.URL.Path]
		if !found {
			http.Error(w, `{"message":"No such container"}`, http.StatusNotFound)
			return
		}
		_, _ = w.Write([]byte(answer))
	}))
	t.Cleanup(server.Close)
	return &Docker{host: images.DockerHost{Address: server.URL}, transport: &http.Transport{
		DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "tcp", server.Listener.Addr().String())
		},
	}}
}

func TestAContainerIsReachedAtTheAddressItHoldsOnItsProjectNetwork(t *testing.T) {
	t.Parallel()
	docker := dockerAnswering(t, map[string]string{
		"/containers/shop-worker/json":  `{"State":{"Running":true},"NetworkSettings":{"Networks":{"bridge":{"IPAddress":"172.17.0.9"},"ocel-production-shop":{"IPAddress":"172.20.0.5"}}}}`,
		"/containers/shop-stopped/json": `{"State":{"Running":false},"NetworkSettings":{"Networks":{"ocel-production-shop":{"IPAddress":""}}}}`,
		"/containers/blog-worker/json":  `{"State":{"Running":true},"NetworkSettings":{"Networks":{"ocel-production-blog":{"IPAddress":"172.21.0.5"}}}}`,
	})
	address, err := docker.Address(context.Background(), "ocel-production-shop", "shop-worker")
	if err != nil || address != "172.20.0.5" {
		t.Errorf("Address(shop-worker) = %q, %v, want 172.20.0.5", address, err)
	}
	for _, container := range []string{"shop-stopped", "shop-gone", "blog-worker"} {
		if address, err := docker.Address(context.Background(), "ocel-production-shop", container); err == nil {
			t.Errorf("Address(%s) = %q, want it refused: nothing answers there", container, address)
		}
	}
}
