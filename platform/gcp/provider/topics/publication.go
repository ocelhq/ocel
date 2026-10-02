package topics

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"strconv"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	pubsub "google.golang.org/api/pubsub/v1"
)

type publication struct {
	topicName   string
	topic       *provider.TopicSpec
	messageID   string
	publishedAt time.Time
	dueAt       time.Time
	payload     json.RawMessage
	key         string
	lane        string
	maxAttempts int32
	consumer    string
	delayTask   string
}

func (p publication) message() *pubsub.PubsubMessage {
	attributes := map[string]string{
		MessageAttribute:     p.messageID,
		PublishedAtAttribute: p.publishedAt.UTC().Format(time.RFC3339Nano),
		DueAtAttribute:       p.dueAt.UTC().Format(time.RFC3339Nano),
		LaneAttribute:        p.lane,
	}
	if p.maxAttempts > 0 {
		attributes[MaxAttemptsAttribute] = strconv.Itoa(int(p.maxAttempts))
	}
	if p.consumer != "" {
		attributes[ConsumerAttribute] = p.consumer
	}
	if p.delayTask != "" {
		attributes[DelayTaskAttribute] = p.delayTask
	}
	message := &pubsub.PubsubMessage{Data: base64Of(p.payload), Attributes: attributes}
	if p.topic.Ordered {
		message.OrderingKey = p.key
	}
	return message
}

func (d Deployment) publishNow(ctx context.Context, p publication) error {
	service, err := d.Clients.PubSub()
	if err != nil {
		return err
	}
	topic := TopicPath(d.Clients.Project, d.Names.Topic(p.topicName))
	err = retried(ctx, func() error {
		_, err := service.Projects.Topics.Publish(topic, &pubsub.PublishRequest{Messages: []*pubsub.PubsubMessage{p.message()}}).Context(ctx).Do()
		return err
	})
	if err != nil {
		return fmt.Errorf("publish message %s to %s: %w", p.messageID, topic, err)
	}
	return nil
}

func base64Of(payload []byte) string { return base64.StdEncoding.EncodeToString(payload) }
