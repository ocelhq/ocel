package topics

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	pubsub "google.golang.org/api/pubsub/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

var _ runs.Queue = Queue{}

type Queue struct {
	Clients *ports.Clients
	Names   Names
	Topics  map[string]*provider.TopicSpec
	Delays  Delays
}

func (q Queue) Send(ctx context.Context, msg runs.Message) error {
	topic, found := q.Topics[msg.Topic]
	if !found {
		return fmt.Errorf("no topic %q is deployed to send message %s to", msg.Topic, msg.ID)
	}
	message := pubsubMessageOf(msg, topic.Ordered)
	if msg.DueAt.After(time.Now()) {
		return q.schedule(ctx, msg, message)
	}
	return q.publish(ctx, msg, message)
}

func (Queue) Release(context.Context, string, string, string) error { return nil }

func (q Queue) topicPath(topic string) (string, error) {
	return q.Names.topicPath(q.Clients.Project, topic)
}

func (q Queue) publish(ctx context.Context, msg runs.Message, message *pubsub.PubsubMessage) error {
	service, err := q.Clients.PubSub()
	if err != nil {
		return err
	}
	topic, err := q.topicPath(msg.Topic)
	if err != nil {
		return err
	}
	err = googleretry.Done(ctx, func() error {
		_, err := service.Projects.Topics.Publish(topic, &pubsub.PublishRequest{Messages: []*pubsub.PubsubMessage{message}}).Context(ctx).Do()
		return err
	})
	if err != nil {
		return fmt.Errorf("publish message %s to %s: %w", msg.ID, topic, err)
	}
	return nil
}

func (q Queue) schedule(ctx context.Context, msg runs.Message, message *pubsub.PubsubMessage) error {
	client, err := q.Clients.CloudTasks()
	if err != nil {
		return err
	}
	body, err := json.Marshal(&pubsub.PublishRequest{Messages: []*pubsub.PubsubMessage{message}})
	if err != nil {
		return err
	}
	topic, err := q.topicPath(msg.Topic)
	if err != nil {
		return err
	}
	task := &cloudtaskspb.Task{
		Name:         q.Delays.taskOf(msg),
		ScheduleTime: timestamppb.New(msg.DueAt),
		MessageType: &cloudtaskspb.Task_HttpRequest{HttpRequest: &cloudtaskspb.HttpRequest{
			Url:        q.Delays.PublishURL + "/v1/" + topic + ":publish",
			HttpMethod: cloudtaskspb.HttpMethod_POST,
			Headers:    map[string]string{"Content-Type": "application/json"},
			Body:       body,
			AuthorizationHeader: &cloudtaskspb.HttpRequest_OauthToken{OauthToken: &cloudtaskspb.OAuthToken{
				ServiceAccountEmail: q.Delays.Account,
				Scope:               ports.CloudPlatformScope,
			}},
		}},
	}
	err = googleretry.Done(ctx, func() error {
		_, err := client.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: q.Delays.Queue, Task: task})
		return err
	})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return fmt.Errorf("schedule message %s for %s: %w", msg.ID, msg.DueAt.Format(time.RFC3339), err)
	}
	return nil
}
