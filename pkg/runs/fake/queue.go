package fake

import (
	"context"
	"fmt"
	"strconv"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
)

const receivePoll = 5 * time.Millisecond

type queued struct {
	handle    string
	msg       runs.Message
	visibleAt time.Time
	receives  int
}

type queueKey struct{ topic, consumer string }

type Queue struct {
	topics map[string]*provider.TopicSpec

	mu     sync.Mutex
	queues map[queueKey][]*queued
	next   int
	sent   []runs.Message
}

func NewQueue(topics map[string]*provider.TopicSpec) *Queue {
	return &Queue{topics: topics, queues: map[queueKey][]*queued{}}
}

func (q *Queue) Send(_ context.Context, msg runs.Message) error {
	topic, found := q.topics[msg.Topic]
	if !found {
		return fmt.Errorf("no topic %q to send to", msg.Topic)
	}
	q.mu.Lock()
	defer q.mu.Unlock()
	q.sent = append(q.sent, msg)
	for _, consumer := range topic.Consumers {
		if msg.Consumer != "" && msg.Consumer != consumer.Name {
			continue
		}
		q.next++
		delivered := msg
		delivered.Consumer = consumer.Name
		key := queueKey{msg.Topic, consumer.Name}
		q.queues[key] = append(q.queues[key], &queued{handle: strconv.Itoa(q.next), msg: delivered, visibleAt: msg.DueAt})
	}
	return nil
}

func (q *Queue) Sent() []runs.Message {
	q.mu.Lock()
	defer q.mu.Unlock()
	return append([]runs.Message(nil), q.sent...)
}

func (q *Queue) Held(topic, consumer string) int {
	q.mu.Lock()
	defer q.mu.Unlock()
	return len(q.queues[queueKey{topic, consumer}])
}

func (q *Queue) find(topic, consumer, handle string) (*queued, int) {
	for i, message := range q.queues[queueKey{topic, consumer}] {
		if message.handle == handle {
			return message, i
		}
	}
	return nil, -1
}

func (q *Queue) Release(_ context.Context, topic, consumer, handle string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if message, _ := q.find(topic, consumer, handle); message != nil {
		message.visibleAt = time.Now()
	}
	return nil
}

func (q *Queue) Receive(ctx context.Context, req runs.ReceiveRequest) ([]runs.Received, error) {
	deadline := time.Now().Add(req.Wait)
	for {
		if received := q.take(req); len(received) > 0 {
			return received, nil
		}
		if !time.Now().Before(deadline) {
			return nil, nil
		}
		select {
		case <-ctx.Done():
			return nil, ctx.Err()
		case <-time.After(min(receivePoll, time.Until(deadline))):
		}
	}
}

func (q *Queue) take(req runs.ReceiveRequest) []runs.Received {
	q.mu.Lock()
	defer q.mu.Unlock()
	now := time.Now()
	ordered := q.topics[req.Topic] != nil && q.topics[req.Topic].Ordered
	blocked := map[string]bool{}
	var received []runs.Received
	for _, message := range q.queues[queueKey{req.Topic, req.Consumer}] {
		if len(received) == req.Max {
			break
		}
		group := message.msg.Key
		if group == "" {
			group = message.handle
		}
		visible := !message.visibleAt.After(now)
		if ordered && blocked[group] {
			continue
		}
		if !visible {
			blocked[group] = true
			continue
		}
		message.receives++
		message.visibleAt = now.Add(req.Hidden)
		received = append(received, runs.Received{Handle: message.handle, Message: message.msg, Receives: message.receives})
	}
	return received
}

func (q *Queue) Extend(_ context.Context, topic, consumer string, handles []string, hidden time.Duration) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, handle := range handles {
		if message, _ := q.find(topic, consumer, handle); message != nil {
			message.visibleAt = time.Now().Add(hidden)
		}
	}
	return nil
}

func (q *Queue) Ack(_ context.Context, topic, consumer string, handles []string) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	for _, handle := range handles {
		if _, i := q.find(topic, consumer, handle); i >= 0 {
			key := queueKey{topic, consumer}
			q.queues[key] = append(q.queues[key][:i], q.queues[key][i+1:]...)
		}
	}
	return nil
}

func (q *Queue) Hide(_ context.Context, topic, consumer, handle string, hidden time.Duration) error {
	q.mu.Lock()
	defer q.mu.Unlock()
	if message, _ := q.find(topic, consumer, handle); message != nil {
		message.visibleAt = time.Now().Add(hidden)
	}
	return nil
}
