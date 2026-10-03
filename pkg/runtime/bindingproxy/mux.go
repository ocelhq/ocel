package bindingproxy

import (
	"net/http"

	connect "connectrpc.com/connect"
	"connectrpc.com/validate"

	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	"github.com/ocelhq/ocel/pkg/proto/app/realtime/v1/realtimev1connect"
	"github.com/ocelhq/ocel/pkg/proto/app/task/v1/taskv1connect"
	"github.com/ocelhq/ocel/pkg/proto/app/topic/v1/topicv1connect"
)

type Services struct {
	Buckets  bucketv1connect.BucketServiceHandler
	Tasks    taskv1connect.TaskServiceHandler
	Topics   topicv1connect.TopicServiceHandler
	Realtime realtimev1connect.RealtimeServiceHandler
}

func (s Services) Empty() bool {
	return s.Buckets == nil && s.Tasks == nil && s.Topics == nil && s.Realtime == nil
}

func NewMux(token string, services Services) *http.ServeMux {
	mux := http.NewServeMux()
	gated := connect.WithInterceptors(&tokenGate{token: token}, validate.NewInterceptor())
	if services.Buckets != nil {
		mux.Handle(bucketv1connect.NewBucketServiceHandler(services.Buckets, gated))
	}
	if services.Tasks != nil {
		mux.Handle(taskv1connect.NewTaskServiceHandler(services.Tasks, gated))
	}
	if services.Topics != nil {
		mux.Handle(topicv1connect.NewTopicServiceHandler(services.Topics, gated))
	}
	if services.Realtime != nil {
		mux.Handle(realtimev1connect.NewRealtimeServiceHandler(services.Realtime, gated))
	}
	return mux
}
