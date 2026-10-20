package googletest

import (
	"encoding/json"
	"net/http"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	pubsub "google.golang.org/api/pubsub/v1"
)

const pullWait = 200 * time.Millisecond

type PubSub struct {
	Mu            sync.Mutex
	Topics        map[string]*pubsub.Policy
	Subscriptions map[string]*pubsub.Subscription
	Created       []string
	Deleted       []string
	Published     int
	DeniedCreates int
	PublishedAgo  time.Duration

	TierTags      *TierTags
	InlineTags    map[string]map[string]string
	DeletedTagged []string

	pending map[string][]*pubsub.ReceivedMessage
	arrived map[string]chan struct{}
	acked   int
	ackedTo chan struct{}
	stall   time.Duration
	stalled chan struct{}
	nextID  int
}

func NewPubSub() *PubSub {
	return &PubSub{
		Topics:        map[string]*pubsub.Policy{},
		Subscriptions: map[string]*pubsub.Subscription{},
		pending:       map[string][]*pubsub.ReceivedMessage{},
		arrived:       map[string]chan struct{}{},
		ackedTo:       make(chan struct{}),
	}
}

func IsPubSubPath(path string) bool {
	return strings.HasPrefix(path, "/v1/projects/") && (strings.Contains(path, "/topics/") || strings.Contains(path, "/subscriptions/"))
}

func (p *PubSub) Serve(t *testing.T) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		name, verb, _ := strings.Cut(strings.TrimPrefix(r.URL.Path, "/v1/"), ":")
		switch {
		case strings.Contains(name, "/topics/"):
			p.serveTopic(t, w, r, name, verb)
		case strings.Contains(name, "/subscriptions/"):
			p.serveSubscription(t, w, r, name, verb)
		default:
			t.Errorf("Pub/Sub was called %s %s, which nothing here serves", r.Method, r.URL.Path)
			w.WriteHeader(http.StatusNotFound)
		}
	}
}

func answerPubSub(w http.ResponseWriter, code int, status string) {
	w.WriteHeader(code)
	_, _ = w.Write([]byte(`{"error":{"code":` + strconv.Itoa(code) + `,"status":"` + status + `","message":"` + status + `"}}`))
}

func (p *PubSub) serveTopic(t *testing.T, w http.ResponseWriter, r *http.Request, topic, verb string) {
	p.Mu.Lock()
	defer p.Mu.Unlock()
	policy, exists := p.Topics[topic]
	switch {
	case r.Method == http.MethodPut && verb == "":
		if exists {
			answerPubSub(w, http.StatusConflict, "ALREADY_EXISTS")
			return
		}
		var asked pubsub.Topic
		_ = json.NewDecoder(r.Body).Decode(&asked)
		p.tagInline(topic, asked.Tags)
		p.Topics[topic] = &pubsub.Policy{Etag: "BwXhoLA="}
		_ = json.NewEncoder(w).Encode(&pubsub.Topic{Name: topic})
	case !exists:
		answerPubSub(w, http.StatusNotFound, "NOT_FOUND")
	case r.Method == http.MethodGet && verb == "":
		_ = json.NewEncoder(w).Encode(&pubsub.Topic{Name: topic})
	case r.Method == http.MethodDelete && verb == "":
		p.noteTagged(topic)
		delete(p.Topics, topic)
		p.Deleted = append(p.Deleted, topic)
		_, _ = w.Write([]byte(`{}`))
	case verb == "getIamPolicy":
		_ = json.NewEncoder(w).Encode(policy)
	case verb == "setIamPolicy":
		var asked pubsub.SetIamPolicyRequest
		if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		p.Topics[topic] = asked.Policy
		_ = json.NewEncoder(w).Encode(asked.Policy)
	case verb == "publish":
		var asked pubsub.PublishRequest
		if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		ids := make([]string, 0, len(asked.Messages))
		for _, message := range asked.Messages {
			p.nextID++
			message.MessageId = strconv.Itoa(p.nextID)
			message.PublishTime = time.Now().Add(-p.PublishedAgo).UTC().Format(time.RFC3339Nano)
			ids = append(ids, message.MessageId)
			for name, subscription := range p.Subscriptions {
				if subscription.Topic == topic {
					p.pending[name] = append(p.pending[name], &pubsub.ReceivedMessage{AckId: name + "/" + message.MessageId, Message: message})
					p.wake(name)
				}
			}
		}
		p.Published += len(asked.Messages)
		_ = json.NewEncoder(w).Encode(&pubsub.PublishResponse{MessageIds: ids})
	default:
		t.Errorf("Pub/Sub was called %s %s, which nothing here serves", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func PubSubTagParent(path string) string { return "//pubsub.googleapis.com/" + path }

func (p *PubSub) tagInline(path string, tags map[string]string) {
	if len(tags) == 0 {
		return
	}
	if p.InlineTags == nil {
		p.InlineTags = map[string]map[string]string{}
	}
	p.InlineTags[path] = tags
	if p.TierTags != nil {
		p.TierTags.BindInline(PubSubTagParent(path), tags)
	}
}

func (p *PubSub) noteTagged(path string) {
	if p.TierTags != nil && len(p.TierTags.Bound(PubSubTagParent(path))) > 0 {
		p.DeletedTagged = append(p.DeletedTagged, path)
	}
}

func (p *PubSub) wake(subscription string) {
	if arrived := p.arrived[subscription]; arrived != nil {
		close(arrived)
		delete(p.arrived, subscription)
	}
}

func (p *PubSub) serveSubscription(t *testing.T, w http.ResponseWriter, r *http.Request, name, verb string) {
	p.Mu.Lock()
	_, exists := p.Subscriptions[name]
	switch {
	case r.Method == http.MethodPut && verb == "":
		defer p.Mu.Unlock()
		var asked pubsub.Subscription
		if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if exists {
			answerPubSub(w, http.StatusConflict, "ALREADY_EXISTS")
			return
		}
		if p.DeniedCreates > 0 {
			p.DeniedCreates--
			answerPubSub(w, http.StatusForbidden, "PERMISSION_DENIED")
			return
		}
		if _, attached := p.Topics[asked.Topic]; !attached {
			answerPubSub(w, http.StatusNotFound, "NOT_FOUND")
			return
		}
		asked.Name = name
		p.tagInline(name, asked.Tags)
		p.Subscriptions[name] = &asked
		p.Created = append(p.Created, name)
		_ = json.NewEncoder(w).Encode(&asked)
	case r.Method == http.MethodDelete && verb == "":
		defer p.Mu.Unlock()
		if !exists {
			answerPubSub(w, http.StatusNotFound, "NOT_FOUND")
			return
		}
		p.noteTagged(name)
		delete(p.Subscriptions, name)
		delete(p.pending, name)
		p.wake(name)
		p.Deleted = append(p.Deleted, name)
		_, _ = w.Write([]byte(`{}`))
	case r.Method == http.MethodGet && verb == "":
		defer p.Mu.Unlock()
		if !exists {
			answerPubSub(w, http.StatusNotFound, "NOT_FOUND")
			return
		}
		_ = json.NewEncoder(w).Encode(p.Subscriptions[name])
	case r.Method == http.MethodPatch && verb == "":
		defer p.Mu.Unlock()
		if !exists {
			answerPubSub(w, http.StatusNotFound, "NOT_FOUND")
			return
		}
		_ = json.NewEncoder(w).Encode(p.Subscriptions[name])
	case verb == "pull":
		p.pull(w, r, name, exists)
	case verb == "acknowledge":
		defer p.Mu.Unlock()
		var asked pubsub.AcknowledgeRequest
		if err := json.NewDecoder(r.Body).Decode(&asked); err != nil {
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		if stall, stalled := p.stall, p.stalled; stall > 0 {
			p.stall, p.stalled = 0, nil
			close(stalled)
			p.Mu.Unlock()
			select {
			case <-r.Context().Done():
			case <-time.After(stall):
			}
			p.Mu.Lock()
		}
		p.acked += len(asked.AckIds)
		close(p.ackedTo)
		p.ackedTo = make(chan struct{})
		_, _ = w.Write([]byte(`{}`))
	default:
		p.Mu.Unlock()
		t.Errorf("Pub/Sub was called %s %s, which nothing here serves", r.Method, r.URL.Path)
		w.WriteHeader(http.StatusNotFound)
	}
}

func (p *PubSub) pull(w http.ResponseWriter, r *http.Request, name string, exists bool) {
	if !exists {
		p.Mu.Unlock()
		answerPubSub(w, http.StatusNotFound, "NOT_FOUND")
		return
	}
	if len(p.pending[name]) == 0 {
		arrived := p.arrived[name]
		if arrived == nil {
			arrived = make(chan struct{})
			p.arrived[name] = arrived
		}
		p.Mu.Unlock()
		select {
		case <-arrived:
		case <-r.Context().Done():
		case <-time.After(pullWait):
		}
		p.Mu.Lock()
	}
	received := p.pending[name]
	delete(p.pending, name)
	p.Mu.Unlock()
	_ = json.NewEncoder(w).Encode(&pubsub.PullResponse{ReceivedMessages: received})
}

func (p *PubSub) StallNextAcknowledge(stall time.Duration) <-chan struct{} {
	p.Mu.Lock()
	defer p.Mu.Unlock()
	p.stall, p.stalled = stall, make(chan struct{})
	return p.stalled
}

func (p *PubSub) AwaitAcknowledged(count int, within time.Duration) int {
	deadline := time.After(within)
	for {
		p.Mu.Lock()
		acked, more := p.acked, p.ackedTo
		p.Mu.Unlock()
		if acked >= count {
			return acked
		}
		select {
		case <-more:
		case <-deadline:
			return acked
		}
	}
}

func (p *PubSub) SubscriptionsOf(topic string) []*pubsub.Subscription {
	p.Mu.Lock()
	defer p.Mu.Unlock()
	var attached []*pubsub.Subscription
	for _, subscription := range p.Subscriptions {
		if subscription.Topic == topic {
			attached = append(attached, subscription)
		}
	}
	slices.SortFunc(attached, func(a, b *pubsub.Subscription) int { return strings.Compare(a.Name, b.Name) })
	return attached
}

func (p *PubSub) TopicMembers(topic, role string) []string {
	p.Mu.Lock()
	defer p.Mu.Unlock()
	policy := p.Topics[topic]
	if policy == nil {
		return nil
	}
	for _, binding := range policy.Bindings {
		if binding.Role == role {
			return binding.Members
		}
	}
	return nil
}

func (p *PubSub) DropSubscription(name string) {
	p.Mu.Lock()
	defer p.Mu.Unlock()
	delete(p.Subscriptions, name)
	delete(p.pending, name)
	p.wake(name)
}

func (p *PubSub) AddTopic(topic string) {
	p.Mu.Lock()
	defer p.Mu.Unlock()
	p.Topics[topic] = &pubsub.Policy{Etag: "BwXhoLA="}
}

func (p *PubSub) HasTopic(topic string) bool {
	p.Mu.Lock()
	defer p.Mu.Unlock()
	_, exists := p.Topics[topic]
	return exists
}
