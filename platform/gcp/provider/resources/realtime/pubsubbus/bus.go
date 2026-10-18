package pubsubbus

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"slices"
	"strings"
	"sync"
	"time"

	"google.golang.org/api/option"
	pubsub "google.golang.org/api/pubsub/v1"

	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
)

const (
	TopicVar    = "OCEL_REALTIME_TOPIC"
	EndpointVar = "OCEL_REALTIME_PUBSUB_ENDPOINT"

	SubscriptionTTL = 24 * time.Hour

	messageRetention   = 10 * time.Minute
	ackDeadline        = 10 * time.Second
	pulledAtMost       = 1000
	pullBackoffCeiling = 10 * time.Second
	channelAttribute   = "channel"
	suffixBytes        = 8
)

var unseenAnswers = []int{http.StatusForbidden, http.StatusNotFound}

type Bus struct {
	service      *pubsub.Service
	topic        string
	subscription string

	mu        sync.RWMutex
	listeners map[*listener]struct{}

	stopPulling context.CancelFunc
	pulled      chan struct{}
	closing     sync.Once
}

type listener struct {
	deliver func(channel, event string)
}

func Open(ctx context.Context, topic string, options ...option.ClientOption) (*Bus, error) {
	project, _, isTopic := strings.Cut(strings.TrimPrefix(topic, "projects/"), "/topics/")
	if !strings.HasPrefix(topic, "projects/") || !isTopic || project == "" {
		return nil, fmt.Errorf("%s names %q, not a Pub/Sub topic projects/<project>/topics/<topic>", TopicVar, topic)
	}
	service, err := pubsub.NewService(ctx, options...)
	if err != nil {
		return nil, fmt.Errorf("open the Pub/Sub client: %w", err)
	}
	suffix := make([]byte, suffixBytes)
	if _, err := rand.Read(suffix); err != nil {
		return nil, fmt.Errorf("name this instance's subscription: %w", err)
	}
	b := &Bus{
		service:      service,
		topic:        topic,
		subscription: "projects/" + project + "/subscriptions/" + topic[strings.LastIndex(topic, "/")+1:] + "-" + hex.EncodeToString(suffix),
		listeners:    map[*listener]struct{}{},
		pulled:       make(chan struct{}),
	}
	if err := b.subscribeOnceVisible(ctx); err != nil {
		return nil, err
	}
	pulling, stop := context.WithCancel(context.WithoutCancel(ctx))
	b.stopPulling = stop
	go b.pull(pulling)
	return b, nil
}

func (b *Bus) subscribe(ctx context.Context) error {
	desired := &pubsub.Subscription{
		Topic:                    b.topic,
		AckDeadlineSeconds:       int64(ackDeadline / time.Second),
		MessageRetentionDuration: durationText(messageRetention),
		ExpirationPolicy:         &pubsub.ExpirationPolicy{Ttl: durationText(SubscriptionTTL)},
	}
	_, err := googleretry.Attempted(ctx, b.service.Projects.Subscriptions.Create(b.subscription, desired).Context(ctx).Do)
	if err != nil && googleretry.AnsweredCode(err) != http.StatusConflict {
		return fmt.Errorf("subscribe this gateway instance to %s as %s: %w", b.topic, b.subscription, err)
	}
	return nil
}

func (b *Bus) subscribeOnceVisible(ctx context.Context) error {
	for attempt := 1; ; attempt++ {
		err := b.subscribe(ctx)
		if !slices.Contains(unseenAnswers, googleretry.AnsweredCode(err)) {
			return err
		}
		if !googleretry.WaitedFor(ctx, attempt, pullBackoffCeiling) {
			return err
		}
	}
}

func durationText(d time.Duration) string { return fmt.Sprintf("%ds", int64(d/time.Second)) }

func (b *Bus) Publish(ctx context.Context, channel, event string) error {
	message := &pubsub.PubsubMessage{
		Data:       base64.StdEncoding.EncodeToString([]byte(event)),
		Attributes: map[string]string{channelAttribute: channel},
	}
	_, err := googleretry.Attempted(ctx, b.service.Projects.Topics.Publish(b.topic, &pubsub.PublishRequest{Messages: []*pubsub.PubsubMessage{message}}).Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("publish on %s: %w", b.topic, err)
	}
	return nil
}

func (b *Bus) Listen(deliver func(channel, event string)) func() {
	l := &listener{deliver: deliver}
	b.mu.Lock()
	defer b.mu.Unlock()
	b.listeners[l] = struct{}{}
	return func() {
		b.mu.Lock()
		defer b.mu.Unlock()
		delete(b.listeners, l)
	}
}

func (b *Bus) pull(ctx context.Context) {
	defer close(b.pulled)
	failed := 0
	for ctx.Err() == nil {
		answer, err := b.service.Projects.Subscriptions.Pull(b.subscription, &pubsub.PullRequest{MaxMessages: pulledAtMost}).Context(ctx).Do()
		if ctx.Err() != nil {
			return
		}
		if err != nil {
			failed++
			fmt.Fprintf(os.Stderr, "pull %s: %v\n", b.subscription, err)
			if googleretry.AnsweredCode(err) == http.StatusNotFound {
				if err := b.subscribe(ctx); err != nil {
					fmt.Fprintln(os.Stderr, err)
				}
			}
			googleretry.WaitedFor(ctx, failed, pullBackoffCeiling)
			continue
		}
		failed = 0
		b.deliver(answer.ReceivedMessages)
		b.acknowledge(ctx, answer.ReceivedMessages)
	}
}

func (b *Bus) deliver(received []*pubsub.ReceivedMessage) {
	b.mu.RLock()
	defer b.mu.RUnlock()
	for _, each := range received {
		if each.Message == nil {
			continue
		}
		event, err := base64.StdEncoding.DecodeString(each.Message.Data)
		if err != nil {
			fmt.Fprintf(os.Stderr, "drop message %s on %s, whose data is no base64: %v\n", each.Message.MessageId, b.subscription, err)
			continue
		}
		for l := range b.listeners {
			l.deliver(each.Message.Attributes[channelAttribute], string(event))
		}
	}
}

func (b *Bus) acknowledge(ctx context.Context, received []*pubsub.ReceivedMessage) {
	if len(received) == 0 {
		return
	}
	ids := make([]string, 0, len(received))
	for _, each := range received {
		ids = append(ids, each.AckId)
	}
	acking, cancel := context.WithTimeout(context.WithoutCancel(ctx), ackDeadline)
	defer cancel()
	_, err := googleretry.Attempted(acking, b.service.Projects.Subscriptions.Acknowledge(b.subscription, &pubsub.AcknowledgeRequest{AckIds: ids}).Context(acking).Do)
	if err != nil {
		fmt.Fprintf(os.Stderr, "acknowledge %d messages on %s, which Pub/Sub delivers again: %v\n", len(ids), b.subscription, err)
	}
}

func (b *Bus) Close(ctx context.Context) error {
	var err error
	b.closing.Do(func() {
		b.stopPulling()
		<-b.pulled
		_, err = googleretry.Attempted(ctx, b.service.Projects.Subscriptions.Delete(b.subscription).Context(ctx).Do)
		if googleretry.AnsweredCode(err) == http.StatusNotFound {
			err = nil
		}
		if err != nil {
			err = fmt.Errorf("delete this gateway instance's subscription %s, which Pub/Sub reclaims a day after its last pull: %w", b.subscription, err)
		}
	})
	return err
}
