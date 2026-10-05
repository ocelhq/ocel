package queue

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"path/filepath"
	"slices"
	"sync"
	"time"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/declaration"
	"github.com/ocelhq/ocel/cli/internal/devresources/binding"
	"github.com/ocelhq/ocel/cli/internal/devresources/docker"
	"github.com/ocelhq/ocel/cli/internal/devresources/secret"
	"github.com/ocelhq/ocel/cli/internal/manifest"
	"github.com/ocelhq/ocel/cli/internal/project"
	"github.com/ocelhq/ocel/pkg/images"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/app/resources/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/task/v1/taskv1connect"
	"github.com/ocelhq/ocel/pkg/proto/app/topic/v1/topicv1connect"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/pgmq"
)

const (
	backend      = "queue"
	passwordFile = "queue-database-password"
	superuser    = "postgres"
	serverPort   = 5432
	dataPath     = "/var/lib/postgresql"
	readyIn      = 2 * time.Minute
)

var Kinds = []resourcesv1.ResourceType{
	resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC,
	resourcesv1.ResourceType_RESOURCE_TYPE_TASK,
	resourcesv1.ResourceType_RESOURCE_TYPE_CONSUMER,
	resourcesv1.ResourceType_RESOURCE_TYPE_WORKER,
}

type Worker struct {
	Name    string
	Sources []string
}

type Backend struct {
	open       docker.OpenFunc
	secretsDir string
	cfg        *project.Project
	report     func(line string)

	mu            sync.Mutex
	container     *docker.Container
	engine        *pgmq.Engine
	stopDispatch  context.CancelFunc
	dispatched    chan struct{}
	topics        map[string]*contractv1.ManifestTopic
	workers       []Worker
	workerConfigs map[string]*contractv1.ManifestWorker
	workerURLs    map[string]string
}

func New(open docker.OpenFunc, secretsDir string, cfg *project.Project, report func(line string)) *Backend {
	return &Backend{open: open, secretsDir: secretsDir, cfg: cfg, report: report}
}

func (b *Backend) Resolve(ctx context.Context, project string, resources []declaration.Resource) ([]binding.Resolved, error) {
	placement, err := manifest.PlaceConsumers(b.cfg, resources)
	if err != nil {
		return nil, err
	}

	b.mu.Lock()
	defer b.mu.Unlock()
	if len(resources) == 0 && b.engine == nil {
		b.topics, b.workers, b.workerConfigs = nil, nil, nil
		return nil, nil
	}
	engine, err := b.running(ctx, project)
	if err != nil {
		return nil, err
	}
	b.topics = placement.Topics
	b.workerConfigs = map[string]*contractv1.ManifestWorker{}
	b.workers = nil
	for _, worker := range placement.Workers {
		b.workerConfigs[worker.GetName()] = worker
		if sources := placement.Sources[worker.GetName()]; len(sources) > 0 {
			b.workers = append(b.workers, Worker{Name: worker.GetName(), Sources: sources})
		}
	}
	if err := engine.Apply(ctx, b.deployment()); err != nil {
		return nil, err
	}

	var out []binding.Resolved
	for _, resource := range resources {
		bound, bindable, err := bind(resource)
		if err != nil {
			return nil, err
		}
		if !bindable {
			continue
		}
		bound.Origin = "pgmq @ " + b.container.Address
		out = append(out, bound)
	}
	return out, nil
}

func bind(resource declaration.Resource) (binding.Resolved, bool, error) {
	properties := &bindingsv1.Binding{Name: resource.Name}
	switch resource.Type {
	case resourcesv1.ResourceType_RESOURCE_TYPE_TASK:
		properties.Properties = &bindingsv1.Binding_Task{Task: &bindingsv1.TaskProperties{}}
	case resourcesv1.ResourceType_RESOURCE_TYPE_TOPIC:
		properties.Properties = &bindingsv1.Binding_Topic{Topic: &bindingsv1.TopicProperties{}}
	default:
		return binding.Resolved{}, false, nil
	}
	bound, err := binding.Encode(resource.Type, properties)
	return bound, true, err
}

func (b *Backend) deployment() pgmq.Deployment {
	deployment := pgmq.Deployment{Slug: b.cfg.Slug, Topics: b.topics, Workers: map[string]pgmq.Worker{}}
	for name, url := range b.workerURLs {
		if worker, declared := b.workerConfigs[name]; declared {
			deployment.Workers[name] = pgmq.Worker{URL: url, Concurrency: int(worker.GetConcurrency())}
		}
	}
	return deployment
}

func (b *Backend) Workers() []Worker {
	b.mu.Lock()
	defer b.mu.Unlock()
	return slices.Clone(b.workers)
}

func (b *Backend) DeliverTo(ctx context.Context, urls map[string]string) error {
	b.mu.Lock()
	defer b.mu.Unlock()
	b.workerURLs = urls
	if b.engine == nil {
		return nil
	}
	if err := b.engine.Apply(ctx, b.deployment()); err != nil {
		return err
	}
	if b.stopDispatch == nil && len(urls) > 0 {
		b.startDispatch()
	}
	return nil
}

func (b *Backend) startDispatch() {
	dispatching, stop := context.WithCancel(context.Background())
	done := make(chan struct{})
	engine := b.engine
	go func() {
		defer close(done)
		_ = engine.Dispatch(dispatching)
	}()
	b.stopDispatch, b.dispatched = stop, done
}

func (b *Backend) running(ctx context.Context, project string) (*pgmq.Engine, error) {
	if b.engine != nil {
		return b.engine, nil
	}
	password, err := secret.Ensure(filepath.Join(b.secretsDir, passwordFile), secret.Purpose{Owner: "queue database", Noun: "password"})
	if err != nil {
		return nil, err
	}
	dockerEngine, err := b.open(ctx)
	if err != nil {
		return nil, err
	}
	if b.container == nil {
		name := docker.Name(project, backend)
		container, err := dockerEngine.Run(ctx, docker.Spec{
			Name:       name,
			Image:      images.QueueDatabase(),
			Env:        []string{"POSTGRES_PASSWORD=" + password},
			Port:       serverPort,
			Volume:     name,
			VolumePath: dataPath,
			Labels:     docker.Labels(project, backend),
		})
		if err != nil {
			return nil, err
		}
		b.container = &container
	}
	err = docker.WaitReady(ctx, dockerEngine, b.container.ID, readyIn, func(ctx context.Context) error {
		_, err := dockerEngine.Exec(ctx, b.container.ID, "pg_isready", "-h", "127.0.0.1", "-U", superuser)
		return err
	})
	if err != nil {
		return nil, fmt.Errorf("the queue database never accepted a connection: %w", err)
	}
	serverURL := (&url.URL{Scheme: "postgres", User: url.UserPassword(superuser, password), Host: b.container.Address, Path: "/postgres", RawQuery: "sslmode=disable"}).String()
	engine, err := pgmq.Open(ctx, pgmq.Config{ServerURL: serverURL, ReportAttempt: func(attempt pgmq.Attempt) { b.report(describeAttempt(attempt)) }})
	if err != nil {
		return nil, fmt.Errorf("open the queue database: %w", err)
	}
	b.engine = engine
	return engine, nil
}

func (b *Backend) current() (*pgmq.Engine, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.engine == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this app declares no task or topic, so there is nothing to trigger or send to"))
	}
	return b.engine, nil
}

func (b *Backend) Routes(mux *http.ServeMux, guard func(http.Handler) http.Handler, options ...connect.HandlerOption) {
	taskPath, taskHandler := taskv1connect.NewTaskServiceHandler(tasks{b}, options...)
	mux.Handle(taskPath, guard(taskHandler))
	topicPath, topicHandler := topicv1connect.NewTopicServiceHandler(topics{b}, options...)
	mux.Handle(topicPath, guard(topicHandler))
}

func (b *Backend) Close(ctx context.Context, stopContainers bool) error {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.stopDispatch != nil {
		b.stopDispatch()
		<-b.dispatched
		b.stopDispatch, b.dispatched = nil, nil
	}
	if b.engine != nil {
		b.engine.Close()
		b.engine = nil
	}
	b.workerURLs = nil
	if b.container == nil {
		return nil
	}
	running := b.container
	b.container = nil
	if !stopContainers {
		return nil
	}
	engine, err := b.open(ctx)
	if err != nil {
		return err
	}
	return engine.Stop(ctx, running.ID)
}
