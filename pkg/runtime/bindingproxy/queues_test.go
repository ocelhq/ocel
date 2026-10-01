package bindingproxy_test

import (
	"context"
	"net/http"
	"testing"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/pkg/localrpc"
	"github.com/ocelhq/ocel/pkg/processenv"
	taskv1 "github.com/ocelhq/ocel/pkg/proto/app/task/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/task/v1/taskv1connect"
	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/topic/v1/topicv1connect"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
)

type countedTasks struct {
	taskv1connect.UnimplementedTaskServiceHandler
	triggered int
}

func (c *countedTasks) Trigger(context.Context, *taskv1.TriggerRequest) (*taskv1.TriggerResponse, error) {
	c.triggered++
	return &taskv1.TriggerResponse{Id: "01JABCDEFGHJKMNPQRSTVWXYZ0"}, nil
}

type countedTopics struct {
	topicv1connect.UnimplementedTopicServiceHandler
	sent int
}

func (c *countedTopics) Send(context.Context, *topicv1.SendRequest) (*topicv1.SendResponse, error) {
	c.sent++
	return &topicv1.SendResponse{MessageId: "01JABCDEFGHJKMNPQRSTVWXYZ0"}, nil
}

func servingQueues(t *testing.T) (address, token string, tasks *countedTasks, topics *countedTopics) {
	t.Helper()
	tasks, topics = &countedTasks{}, &countedTopics{}
	served, err := bindingproxy.Serve(bindingproxy.Services{Tasks: tasks, Topics: topics})
	if err != nil {
		t.Fatalf("Serve: %v", err)
	}
	t.Cleanup(func() { served.Close() })
	return envValue(t, served.Env, processenv.RuntimeAddressEnvVar), envValue(t, served.Env, localrpc.SessionTokenEnvVar), tasks, topics
}

func TestTheProxyTriggersAndSendsForTheAppThatHoldsItsToken(t *testing.T) {
	t.Parallel()
	address, token, tasks, topics := servingQueues(t)
	client := &http.Client{Transport: bearer(token)}

	if _, err := taskv1connect.NewTaskServiceClient(client, address).Trigger(context.Background(), &taskv1.TriggerRequest{Task: "send-email", Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("Trigger with the token: %v", err)
	}
	if _, err := topicv1connect.NewTopicServiceClient(client, address).Send(context.Background(), &topicv1.SendRequest{Topic: "orders", Payload: []byte(`{}`)}); err != nil {
		t.Fatalf("Send with the token: %v", err)
	}
	if tasks.triggered != 1 || topics.sent != 1 {
		t.Errorf("the queue saw %d triggers and %d sends, want one of each", tasks.triggered, topics.sent)
	}
}

func TestADirectCallWithoutTheTokenIsRefusedBeforeItReachesTheQueue(t *testing.T) {
	t.Parallel()
	address, _, tasks, topics := servingQueues(t)

	_, err := taskv1connect.NewTaskServiceClient(http.DefaultClient, address).Trigger(context.Background(), &taskv1.TriggerRequest{Task: "send-email", Payload: []byte(`{}`)})
	if code := connect.CodeOf(err); code != connect.CodeUnauthenticated {
		t.Errorf("Trigger without the token = %v (%s), want %s", err, code, connect.CodeUnauthenticated)
	}
	_, err = topicv1connect.NewTopicServiceClient(&http.Client{Transport: bearer("guessed")}, address).Send(context.Background(), &topicv1.SendRequest{Topic: "orders", Payload: []byte(`{}`)})
	if code := connect.CodeOf(err); code != connect.CodeUnauthenticated {
		t.Errorf("Send with a guessed token = %v (%s), want %s", err, code, connect.CodeUnauthenticated)
	}
	if tasks.triggered != 0 || topics.sent != 0 {
		t.Errorf("an unauthenticated caller reached the queue: %d triggers, %d sends", tasks.triggered, topics.sent)
	}
}
