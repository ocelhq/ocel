package topics

import (
	"context"
	"encoding/json"
	"fmt"
	"strconv"
	"strings"
	"time"

	"cloud.google.com/go/cloudtasks/apiv2/cloudtaskspb"
	pubsub "google.golang.org/api/pubsub/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const PublishURL = "https://pubsub.googleapis.com"

type Delays struct {
	Queue      string
	Account    string
	PublishURL string
}

func (d Delays) Ensure(ctx context.Context, clients *ports.Clients) error {
	client, err := clients.CloudTasks()
	if err != nil {
		return err
	}
	parent, _, _ := strings.Cut(d.Queue, "/queues/")
	err = retried(ctx, func() error {
		_, err := client.CreateQueue(ctx, &cloudtaskspb.CreateQueueRequest{Parent: parent, Queue: &cloudtaskspb.Queue{Name: d.Queue}})
		return err
	})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return fmt.Errorf("create Cloud Tasks queue %s: %w", d.Queue, err)
	}
	return nil
}

func (d Deployment) delayTaskOf(p publication) string {
	if !p.dueAt.After(time.Now()) {
		return ""
	}
	return d.Delays.Queue + "/tasks/" + p.messageID + "-" + strconv.FormatInt(time.Now().UnixNano(), 10)
}

func (d Deployment) schedule(ctx context.Context, p publication, name string) error {
	client, err := d.Clients.CloudTasks()
	if err != nil {
		return err
	}
	p.delayTask = name
	body, err := json.Marshal(&pubsub.PublishRequest{Messages: []*pubsub.PubsubMessage{p.message()}})
	if err != nil {
		return err
	}
	task := &cloudtaskspb.Task{
		Name:         name,
		ScheduleTime: timestamppb.New(p.dueAt),
		MessageType: &cloudtaskspb.Task_HttpRequest{HttpRequest: &cloudtaskspb.HttpRequest{
			Url:        d.Delays.PublishURL + "/v1/" + TopicPath(d.Clients.Project, d.Names.Topic(p.topicName)) + ":publish",
			HttpMethod: cloudtaskspb.HttpMethod_POST,
			Headers:    map[string]string{"Content-Type": "application/json"},
			Body:       body,
			AuthorizationHeader: &cloudtaskspb.HttpRequest_OauthToken{OauthToken: &cloudtaskspb.OAuthToken{
				ServiceAccountEmail: d.Delays.Account,
				Scope:               ports.CloudPlatformScope,
			}},
		}},
	}
	err = retried(ctx, func() error {
		_, err := client.CreateTask(ctx, &cloudtaskspb.CreateTaskRequest{Parent: d.Delays.Queue, Task: task})
		return err
	})
	if err != nil && status.Code(err) != codes.AlreadyExists {
		return fmt.Errorf("schedule message %s for %s: %w", p.messageID, p.dueAt.Format(time.RFC3339), err)
	}
	return nil
}

func (d Deployment) unschedule(ctx context.Context, name string) error {
	if name == "" {
		return nil
	}
	client, err := d.Clients.CloudTasks()
	if err != nil {
		return err
	}
	err = retried(ctx, func() error {
		return client.DeleteTask(ctx, &cloudtaskspb.DeleteTaskRequest{Name: name})
	})
	if err != nil && status.Code(err) != codes.NotFound {
		return fmt.Errorf("delete delay task %s: %w", name, err)
	}
	return nil
}

func (d Deployment) publish(ctx context.Context, p publication, delayTask string) error {
	if delayTask != "" {
		return d.schedule(ctx, p, delayTask)
	}
	return d.publishNow(ctx, p)
}
