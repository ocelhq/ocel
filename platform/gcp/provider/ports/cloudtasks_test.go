package ports_test

import (
	"context"
	"net"
	"sync"
	"testing"
	"time"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	"cloud.google.com/go/iam/apiv1/iampb"
	"google.golang.org/grpc"

	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const cloudTasksDeadlineCeiling = 30 * time.Second

type deadlines struct {
	mu    sync.Mutex
	left  map[string]time.Duration
	unset []string
}

func (d *deadlines) record(ctx context.Context, req any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if deadline, set := ctx.Deadline(); set {
		d.left[info.FullMethod] = time.Until(deadline)
	} else {
		d.unset = append(d.unset, info.FullMethod)
	}
	return handler(ctx, req)
}

func TestEveryCloudTasksCallCarriesADeadlineCloudTasksAccepts(t *testing.T) {
	seen := &deadlines{left: map[string]time.Duration{}}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	server := grpc.NewServer(grpc.UnaryInterceptor(seen.record))
	cloudtaskspb.RegisterCloudTasksServer(server, cloudtaskspb.UnimplementedCloudTasksServer{})
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(server.Stop)

	clients := &ports.Clients{Project: "project", Region: "us-central1", Endpoint: "http://" + listener.Addr().String()}
	client, err := clients.CloudTasks()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Minute)
	defer cancel()
	queue := "projects/project/locations/us-central1/queues/delays"
	calls := map[string]func() error{
		"/google.cloud.tasks.v2.CloudTasks/GetIamPolicy": func() error {
			_, err := client.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: queue})
			return err
		},
		"/google.cloud.tasks.v2.CloudTasks/SetIamPolicy": func() error {
			_, err := client.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{Resource: queue, Policy: &iampb.Policy{}})
			return err
		},
		"/google.cloud.tasks.v2.CloudTasks/CreateQueue": func() error {
			_, err := client.CreateQueue(ctx, &cloudtaskspb.CreateQueueRequest{Parent: "projects/project/locations/us-central1", Queue: &cloudtaskspb.Queue{Name: queue}})
			return err
		},
		"/google.cloud.tasks.v2.CloudTasks/CreateTask": func() error {
			_, err := client.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: queue, Task: &cloudtaskspb.Task{}})
			return err
		},
	}
	for method, call := range calls {
		if call() == nil {
			t.Fatalf("%s answered, want the unimplemented fake to refuse it", method)
		}
	}

	if len(seen.unset) > 0 {
		t.Errorf("%v reached Cloud Tasks with no deadline, want one at most %s away", seen.unset, cloudTasksDeadlineCeiling)
	}
	for method := range calls {
		left, ok := seen.left[method]
		if !ok {
			t.Errorf("%s never reached Cloud Tasks", method)
			continue
		}
		if left > cloudTasksDeadlineCeiling {
			t.Errorf("%s reached Cloud Tasks with a deadline %s away, want at most %s: Cloud Tasks refuses a deadline further out", method, left.Round(time.Second), cloudTasksDeadlineCeiling)
		}
	}
}
