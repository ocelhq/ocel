package queues

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"maps"
	"net"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"slices"
	"sync"
	"time"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/containerimage"
	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/proto/app/task/v1/taskv1connect"
	"github.com/ocelhq/ocel/pkg/proto/app/topic/v1/topicv1connect"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/pkg/records"
	"github.com/ocelhq/ocel/pkg/runtime/originguard"
	"github.com/ocelhq/ocel/pkg/seal"
	"github.com/ocelhq/ocel/platform/pgmq"
	"github.com/ocelhq/ocel/platform/vps/provider/boxapi"
)

const DefaultInterval = 2 * time.Second

const (
	queueSocketFile = ".s.PGSQL.5432"
	unixAddressMax  = 107
	linkNameLength  = 16
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
	Address(ctx context.Context, network, container string) (string, error)
}

type Mounts interface {
	MountSource(ctx context.Context, container, destination string) (string, error)
}

type Engines struct {
	Records     records.Store
	Tiers       []environment.Tier
	Cipher      Cipher
	Addresses   Addresses
	Mounts      Mounts
	Links       string
	Open        func(ctx context.Context, cfg pgmq.Config) (Engine, error)
	IsAnswering func(ctx context.Context, url string) bool
	Interval    time.Duration

	mu       sync.Mutex
	served   map[queueID]*served
	answered map[workerAddress]bool
}

type workerAddress struct {
	container string
	url       string
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

func (e *Engines) Run(ctx context.Context) {
	interval := e.Interval
	if interval <= 0 {
		interval = DefaultInterval
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		if err := e.Reconcile(ctx); err != nil && ctx.Err() == nil {
			slog.Warn("serve the box's queues", "error", err)
		}
		select {
		case <-ctx.Done():
			e.Close()
			return
		case <-ticker.C:
		}
	}
}

func (e *Engines) Served(tier environment.Tier, project, env string) (taskv1connect.TaskServiceHandler, topicv1connect.TopicServiceHandler, bool) {
	e.mu.Lock()
	defer e.mu.Unlock()
	queue, found := e.served[queueID{tier: tier, project: project, env: env}]
	if !found {
		return nil, nil, false
	}
	tasks, topics := queue.engine.Handlers()
	return tasks, topics, true
}

func (e *Engines) Reconcile(ctx context.Context) error {
	var failed []error
	recorded := map[queueID]bool{}
	running := map[string]bool{}
	linked := map[string]bool{}
	for _, tier := range e.Tiers {
		queues, err := boxapi.ReadQueues(ctx, e.Records, tier)
		if err != nil {
			failed = append(failed, fmt.Errorf("read the %s queues: %w", tier, err))
			continue
		}
		for _, queue := range queues {
			id := queueID{tier: queue.Tier, project: queue.Project, env: queue.Env}
			recorded[id] = true
			for _, worker := range queue.Workers {
				running[worker.Container] = true
			}
			if err := e.serve(ctx, id, queue, linked); err != nil {
				failed = append(failed, fmt.Errorf("serve %s's %s queue: %w", queue.Project, queue.Env, err))
			}
		}
	}
	e.mu.Lock()
	for id, queue := range e.served {
		if !recorded[id] {
			delete(e.served, id)
			queue.close()
		}
	}
	for address := range e.answered {
		if !running[address.container] {
			delete(e.answered, address)
		}
	}
	e.mu.Unlock()
	if err := e.removeLinksBut(linked); err != nil {
		failed = append(failed, err)
	}
	return errors.Join(failed...)
}

func (e *Engines) serve(ctx context.Context, id queueID, queue boxapi.Queue, linked map[string]bool) error {
	serverURL, err := e.openServerURL(ctx, queue, linked)
	if err != nil {
		e.forget(id)
		return err
	}
	deployment, err := e.readDeployment(ctx, queue)
	if err != nil {
		return err
	}
	fingerprint, err := json.Marshal(deployment)
	if err != nil {
		return err
	}

	e.mu.Lock()
	current, found := e.served[id]
	e.mu.Unlock()
	if found && current.serverURL != serverURL {
		e.forget(id)
		found = false
	}
	if !found {
		engine, err := e.Open(ctx, pgmq.Config{ServerURL: serverURL, Database: boxapi.QueueDatabaseName})
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
		e.mu.Lock()
		if e.served == nil {
			e.served = map[queueID]*served{}
		}
		e.served[id] = current
		e.mu.Unlock()
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

func (e *Engines) openServerURL(ctx context.Context, queue boxapi.Queue, linked map[string]bool) (string, error) {
	sockets, err := e.Mounts.MountSource(ctx, queue.Database.Container, boxapi.QueueSocketsPath)
	if err != nil {
		return "", fmt.Errorf("find the socket of the queue database %s: %w", queue.Database.Container, err)
	}
	host, err := e.ensureReachable(sockets, linked)
	if err != nil {
		return "", fmt.Errorf("reach the socket of the queue database %s: %w", queue.Database.Container, err)
	}
	return (&url.URL{
		Scheme:   "postgres",
		Path:     "/" + boxapi.QueueDatabaseName,
		RawQuery: url.Values{"host": {host}, "user": {boxapi.QueueRole}, "sslmode": {"disable"}}.Encode(),
	}).String(), nil
}

func fitsUnixAddress(dir string) bool {
	return len(filepath.Join(dir, queueSocketFile)) <= unixAddressMax
}

func (e *Engines) ensureReachable(sockets string, linked map[string]bool) (string, error) {
	if fitsUnixAddress(sockets) {
		return sockets, nil
	}
	if e.Links == "" {
		return "", fmt.Errorf("its socket under %s is longer than the %d bytes a unix address holds, and this box keeps no directory to link it from", sockets, unixAddressMax)
	}
	sum := sha256.Sum256([]byte(sockets))
	name := hex.EncodeToString(sum[:])[:linkNameLength]
	link := filepath.Join(e.Links, name)
	if !fitsUnixAddress(link) {
		return "", fmt.Errorf("its socket under %s is longer than the %d bytes a unix address holds, and so is a link to it from %s", sockets, unixAddressMax, e.Links)
	}
	linked[name] = true
	if current, err := os.Readlink(link); err == nil && current == sockets {
		return link, nil
	}
	if err := os.Remove(link); err != nil && !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	if err := os.Symlink(sockets, link); err != nil {
		return "", err
	}
	return link, nil
}

func (e *Engines) removeLinksBut(linked map[string]bool) error {
	if e.Links == "" {
		return nil
	}
	entries, err := os.ReadDir(e.Links)
	if err != nil {
		return fmt.Errorf("read the queue socket links in %s: %w", e.Links, err)
	}
	var failed []error
	for _, entry := range entries {
		if linked[entry.Name()] {
			continue
		}
		if err := os.Remove(filepath.Join(e.Links, entry.Name())); err != nil && !errors.Is(err, os.ErrNotExist) {
			failed = append(failed, err)
		}
	}
	return errors.Join(failed...)
}

func (e *Engines) openDeliverySecret(ctx context.Context, queue boxapi.Queue) (string, error) {
	sealed, err := base64.StdEncoding.DecodeString(queue.Database.DeliverySealed)
	if err != nil || len(sealed) == 0 {
		return "", fmt.Errorf("the queue database %s records no delivery secret its workers admit", queue.Database.Container)
	}
	bound, err := boxapi.NewQueueDeliverySecretAssociatedData(queue.Project, queue.Tier, queue.Database.Stack)
	if err != nil {
		return "", err
	}
	secret, err := e.Cipher.Open(ctx, queue.Tier, bound, sealed)
	if err != nil {
		return "", fmt.Errorf("open the queue's delivery secret: %w", err)
	}
	return string(secret), nil
}

func (e *Engines) readDeployment(ctx context.Context, queue boxapi.Queue) (pgmq.Deployment, error) {
	deployment := pgmq.Deployment{Slug: queue.Project, Topics: map[string]*contractv1.ManifestTopic{}, Workers: map[string]pgmq.Worker{}}
	for name, raw := range queue.Topics {
		topic := &contractv1.ManifestTopic{}
		if err := protojson.Unmarshal(raw, topic); err != nil {
			return pgmq.Deployment{}, fmt.Errorf("read topic %s: %w", name, err)
		}
		deployment.Topics[name] = topic
	}
	if len(queue.Workers) == 0 {
		return deployment, nil
	}
	secret, err := e.openDeliverySecret(ctx, queue)
	if err != nil {
		return pgmq.Deployment{}, err
	}
	signed := http.Header{}
	signed.Set(edge.OriginSecretHeader, secret)
	for _, name := range slices.Sorted(maps.Keys(queue.Workers)) {
		worker := queue.Workers[name]
		address, err := e.Addresses.Address(ctx, boxapi.AppNetwork(queue.Tier, queue.Project), worker.Container)
		if err != nil {
			slog.Warn("find a worker container, so nothing is delivered to it yet", "project", queue.Project, "env", queue.Env, "worker", name, "container", worker.Container, "error", err)
			continue
		}
		url := "http://" + net.JoinHostPort(address, containerimage.PortText)
		if !e.isWorkerAnswering(ctx, worker.Container, url) {
			continue
		}
		deployment.Workers[name] = pgmq.Worker{URL: url, Concurrency: worker.Concurrency, Header: signed}
	}
	return deployment, nil
}

func (e *Engines) isWorkerAnswering(ctx context.Context, container, url string) bool {
	key := workerAddress{container: container, url: url}
	e.mu.Lock()
	answered := e.answered[key]
	e.mu.Unlock()
	if answered {
		return true
	}
	probe := e.IsAnswering
	if probe == nil {
		probe = IsWorkerAnswering
	}
	if !probe(ctx, url) {
		return false
	}
	e.mu.Lock()
	if e.answered == nil {
		e.answered = map[workerAddress]bool{}
	}
	e.answered[key] = true
	e.mu.Unlock()
	return true
}

func (e *Engines) forget(id queueID) {
	e.mu.Lock()
	queue, found := e.served[id]
	delete(e.served, id)
	e.mu.Unlock()
	if found {
		queue.close()
	}
}

func (e *Engines) Close() {
	e.mu.Lock()
	served := e.served
	e.served = nil
	e.mu.Unlock()
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

const answerWindow = 2 * time.Second

func IsWorkerAnswering(ctx context.Context, url string) bool {
	asking, cancel := context.WithTimeout(ctx, answerWindow)
	defer cancel()
	req, err := http.NewRequestWithContext(asking, http.MethodGet, url, nil)
	if err != nil {
		return false
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		return false
	}
	defer resp.Body.Close()
	return resp.Header.Get(originguard.AppUnansweredHeader) == ""
}

func OpenPgmq(ctx context.Context, cfg pgmq.Config) (Engine, error) {
	engine, err := pgmq.Open(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return pgmqEngine{engine}, nil
}
