package queues

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/url"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/proto/app/task/v1/taskv1connect"
	"github.com/ocelhq/ocel/pkg/proto/app/topic/v1/topicv1connect"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/pgmq"
	"github.com/ocelhq/ocel/platform/vps/provider/live"
)

const (
	DefaultInterval = 2 * time.Second

	superuser    = "postgres"
	databasePort = "5432"
)

type Engine interface {
	Apply(ctx context.Context, deployment pgmq.Deployment) error
	Dispatch(ctx context.Context) error
	Close()
	Handlers() (taskv1connect.TaskServiceHandler, topicv1connect.TopicServiceHandler)
}

type Cipher interface {
	Open(ctx context.Context, tier environment.Tier, bound seal.AssociatedData, sealed []byte) ([]byte, error)
}

type Addresses interface {
	Address(ctx context.Context, container string) (string, error)
}

type Engines struct {
	Records   keyvalue.Store
	Tiers     []environment.Tier
	Cipher    Cipher
	Addresses Addresses
	Open      func(ctx context.Context, cfg pgmq.Config) (Engine, error)
	Interval  time.Duration

	mu     sync.Mutex
	served map[queueID]*served
}

type queueID struct {
	tier    environment.Tier
	project string
	env     string
}

type served struct {
	engine    Engine
	serverURL string
	applied   string
	stop      context.CancelFunc
	done      chan struct{}
}

func (h *Engines) Run(ctx context.Context) {
	interval := h.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := h.Reconcile(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("serve the box's queues", "error", err)
		}
		select {
		case <-ctx.Done():
			h.closeAll()
			return
		case <-ticker.C:
		}
	}
}

func (h *Engines) Served(tier environment.Tier, project, env string) (taskv1connect.TaskServiceHandler, topicv1connect.TopicServiceHandler, bool) {
	h.mu.Lock()
	defer h.mu.Unlock()
	queue, found := h.served[queueID{tier: tier, project: project, env: env}]
	if !found {
		return nil, nil, false
	}
	tasks, topics := queue.engine.Handlers()
	return tasks, topics, true
}

func (h *Engines) Reconcile(ctx context.Context) error {
	var failed []error
	recorded := map[queueID]bool{}
	for _, tier := range h.Tiers {
		queues, err := live.ReadQueues(ctx, h.Records, tier)
		if err != nil {
			failed = append(failed, fmt.Errorf("read the %s queues: %w", tier, err))
			continue
		}
		for _, queue := range queues {
			id := queueID{tier: queue.Tier, project: queue.Project, env: queue.Env}
			recorded[id] = true
			if err := h.serve(ctx, id, queue); err != nil {
				failed = append(failed, fmt.Errorf("serve %s's %s queue: %w", queue.Project, queue.Env, err))
			}
		}
	}
	h.mu.Lock()
	for id, queue := range h.served {
		if !recorded[id] {
			delete(h.served, id)
			queue.close()
		}
	}
	h.mu.Unlock()
	return errors.Join(failed...)
}

func (h *Engines) serve(ctx context.Context, id queueID, queue live.Queue) error {
	serverURL, err := h.serverURL(ctx, queue)
	if err != nil {
		h.forget(id)
		return err
	}
	deployment, err := h.deployment(ctx, queue)
	if err != nil {
		return err
	}
	fingerprint, err := json.Marshal(deployment)
	if err != nil {
		return err
	}

	h.mu.Lock()
	current, found := h.served[id]
	h.mu.Unlock()
	if found && current.serverURL != serverURL {
		h.forget(id)
		found = false
	}
	if !found {
		engine, err := h.Open(ctx, pgmq.Config{ServerURL: serverURL, Database: live.QueueResource})
		if err != nil {
			return err
		}
		dispatching, stop := context.WithCancel(context.WithoutCancel(ctx))
		current = &served{engine: engine, serverURL: serverURL, stop: stop, done: make(chan struct{})}
		go func() {
			defer close(current.done)
			if err := engine.Dispatch(dispatching); err != nil && dispatching.Err() == nil {
				slog.Warn("dispatch a queue", "project", id.project, "env", id.env, "error", err)
			}
		}()
		h.mu.Lock()
		if h.served == nil {
			h.served = map[queueID]*served{}
		}
		h.served[id] = current
		h.mu.Unlock()
	}
	if current.applied == string(fingerprint) {
		return nil
	}
	if err := current.engine.Apply(ctx, deployment); err != nil {
		return err
	}
	current.applied = string(fingerprint)
	return nil
}

func (h *Engines) serverURL(ctx context.Context, queue live.Queue) (string, error) {
	address, err := h.Addresses.Address(ctx, queue.Database.Container)
	if err != nil {
		return "", fmt.Errorf("find the queue database %s: %w", queue.Database.Container, err)
	}
	sealed, err := base64.StdEncoding.DecodeString(queue.Database.Sealed)
	if err != nil {
		return "", fmt.Errorf("the queue database's password is not base64: %w", err)
	}
	bound, err := live.NewQueueSecretAssociatedData(queue.Project, queue.Tier, queue.Database.Stack)
	if err != nil {
		return "", err
	}
	password, err := h.Cipher.Open(ctx, queue.Tier, bound, sealed)
	if err != nil {
		return "", fmt.Errorf("open the queue database's password: %w", err)
	}
	return (&url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(superuser, string(password)),
		Host:     net.JoinHostPort(address, databasePort),
		Path:     "/" + superuser,
		RawQuery: "sslmode=disable",
	}).String(), nil
}

func (h *Engines) deployment(ctx context.Context, queue live.Queue) (pgmq.Deployment, error) {
	deployment := pgmq.Deployment{Slug: queue.Project, Topics: map[string]*contractv1.ManifestTopic{}, Workers: map[string]pgmq.Worker{}}
	for name, raw := range queue.Topics {
		topic := &contractv1.ManifestTopic{}
		if err := protojson.Unmarshal(raw, topic); err != nil {
			return pgmq.Deployment{}, fmt.Errorf("read topic %s: %w", name, err)
		}
		deployment.Topics[name] = topic
	}
	for _, name := range slices.Sorted(func(yield func(string) bool) {
		for name := range queue.Workers {
			if !yield(name) {
				return
			}
		}
	}) {
		worker := queue.Workers[name]
		address, err := h.Addresses.Address(ctx, worker.Container)
		if err != nil {
			continue
		}
		deployment.Workers[name] = pgmq.Worker{
			URL:         "http://" + net.JoinHostPort(address, containerimage.PortText),
			Concurrency: worker.Concurrency,
		}
	}
	return deployment, nil
}

func (h *Engines) forget(id queueID) {
	h.mu.Lock()
	queue, found := h.served[id]
	delete(h.served, id)
	h.mu.Unlock()
	if found {
		queue.close()
	}
}

func (h *Engines) closeAll() {
	h.mu.Lock()
	served := h.served
	h.served = nil
	h.mu.Unlock()
	for _, queue := range served {
		queue.close()
	}
}

func (s *served) close() {
	s.stop()
	<-s.done
	s.engine.Close()
}

type pgmqEngine struct{ *pgmq.Engine }

func (e pgmqEngine) Handlers() (taskv1connect.TaskServiceHandler, topicv1connect.TopicServiceHandler) {
	return e.Tasks(), e.Topics()
}

func OpenPgmq(ctx context.Context, cfg pgmq.Config) (Engine, error) {
	engine, err := pgmq.Open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return pgmqEngine{engine}, nil
}
