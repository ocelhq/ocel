package tasks

import (
	"context"
	"net/http"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
)

const (
	cancelPoll       = 500 * time.Millisecond
	inFlightPerQueue = 10
)

type Config struct {
	Topology queues.Topology
	Table    table
	Queues   queueService
	Topics   topicService
	Services serviceControl

	Worker    string
	WorkerURL string
	Client    *http.Client
}

type Engine struct {
	runs   *runs.Engine
	queue  *Queue
	worker string
}

func NewAWS(cfg aws.Config, topology queues.Topology, worker, workerURL string) *Engine {
	return New(Config{
		Topology:  topology,
		Table:     dynamodb.NewFromConfig(cfg),
		Queues:    sqs.NewFromConfig(cfg),
		Topics:    sns.NewFromConfig(cfg),
		Services:  ecs.NewFromConfig(cfg),
		Worker:    worker,
		WorkerURL: workerURL,
	})
}

func New(cfg Config) *Engine {
	topology := cfg.Topology
	return newEngine(cfg, NewStore(cfg.Table, topology.Table, topology.KeyPrefix), NewSlots(cfg.Table, topology.Table, topology.KeyPrefix))
}

func newEngine(cfg Config, store runs.Store, slots runs.Slots) *Engine {
	queue := NewQueue(cfg.Topology, cfg.Queues, cfg.Topics, cfg.Services)
	return &Engine{
		runs: runs.New(runs.Config{
			Store:       store,
			Queue:       queue,
			Slots:       slots,
			Deployment:  deploymentOf(cfg),
			CancelPoll:  cancelPoll,
			InFlight:    inFlightPerQueue,
			MaxReceives: queues.MaxReceiveCount,
		}),
		queue:  queue,
		worker: cfg.Worker,
	}
}

func deploymentOf(cfg Config) runs.Deployment {
	deployment := runs.Deployment{Topics: map[string]*provider.TopicSpec{}, Workers: map[string]runs.Worker{}}
	for name, topic := range cfg.Topology.Topics {
		if topic.Declared != nil {
			deployment.Topics[name] = topic.Declared
		}
	}
	if cfg.Worker != "" {
		deployment.Workers[cfg.Worker] = runs.Worker{URL: cfg.WorkerURL, Client: cfg.Client, Concurrency: cfg.Topology.Workers[cfg.Worker].Concurrency}
	}
	return deployment
}

func (e *Engine) Tasks() runs.Tasks { return e.runs.Tasks() }

func (e *Engine) Topics() runs.Topics { return e.runs.Topics() }

func (e *Engine) FireCron(ctx context.Context, task string, scheduled time.Time) error {
	return e.runs.FireCron(ctx, task, scheduled)
}
