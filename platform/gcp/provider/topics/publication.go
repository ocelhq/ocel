package topics

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"connectrpc.com/connect"
	pubsub "google.golang.org/api/pubsub/v1"

	topicv1 "github.com/ocelhq/ocel/pkg/proto/app/topic/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

type publication struct {
	topicName   string
	topic       *contractv1.ManifestTopic
	messageID   string
	publishedAt time.Time
	dueAt       time.Time
	payload     json.RawMessage
	key         string
	lane        string
	maxAttempts int32
	consumer    string
}

func (p publication) message() *pubsub.PubsubMessage {
	attributes := map[string]string{
		MessageAttribute:     p.messageID,
		PublishedAtAttribute: p.publishedAt.UTC().Format(time.RFC3339Nano),
		LaneAttribute:        p.lane,
	}
	if p.maxAttempts > 0 {
		attributes[MaxAttemptsAttribute] = strconv.Itoa(int(p.maxAttempts))
	}
	if p.consumer != "" {
		attributes[ConsumerAttribute] = p.consumer
	}
	message := &pubsub.PubsubMessage{Data: base64Of(p.payload), Attributes: attributes}
	if p.topic.GetOrdered() {
		message.OrderingKey = p.key
	}
	return message
}

func (d Deployment) publishNow(ctx context.Context, p publication) error {
	service, err := d.Clients.PubSub()
	if err != nil {
		return err
	}
	topic := topicPath(d.Clients.Project, d.Names.Topic(p.topicName))
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

func laneName(lane topicv1.Lane) string {
	switch lane {
	case topicv1.Lane_LANE_HIGH:
		return "high"
	case topicv1.Lane_LANE_LOW:
		return "low"
	default:
		return "default"
	}
}

func refuseNonJSON(payload []byte) error {
	if len(payload) > 0 && !json.Valid(payload) {
		return connect.NewError(connect.CodeInvalidArgument, errors.New("the payload is not JSON"))
	}
	return nil
}
