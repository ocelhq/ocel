package runs

import (
	"context"
	"net/http"
	"sync"
	"time"

	"github.com/ocelhq/ocel/pkg/provider"
)

const (
	DefaultLease         = 2 * time.Minute
	DefaultShutdownGrace = 10 * time.Second
	DefaultInFlight      = 10

	dueSlack    = 250 * time.Millisecond
	retainedFor = 5 * time.Second
)

type Worker struct {
	URL         string
	Client      *http.Client
	Concurrency int
}

type Deployment struct {
	Topics  map[string]*provider.TopicSpec
	Workers map[string]Worker
}

type Config struct {
	Store           Store
	Queue           Queue
	Slots           Slots
	Deployment      Deployment
	Lease           time.Duration
	ShutdownGrace   time.Duration
	InFlight        int
	CancelPoll      time.Duration
	MaxReceives     int
	MaxOrderedDelay time.Duration
	ReportAttempt   func(Attempt)
}

type Engine struct {
	cfg Config

	mu          sync.Mutex
	deployment  Deployment
	workerSlots map[string]*localSlots
	inFlight    map[string]context.CancelCauseFunc
}

func New(cfg Config) *Engine {
	if cfg.Lease <= 0 {
		cfg.Lease = DefaultLease
	}
	if cfg.ShutdownGrace <= 0 {
		cfg.ShutdownGrace = DefaultShutdownGrace
	}
	if cfg.InFlight <= 0 {
		cfg.InFlight = DefaultInFlight
	}
	if cfg.ReportAttempt == nil {
		cfg.ReportAttempt = func(Attempt) {}
	}
	e := &Engine{cfg: cfg, workerSlots: map[string]*localSlots{}, inFlight: map[string]context.CancelCauseFunc{}}
	e.Deploy(cfg.Deployment)
	return e
}

func (e *Engine) Deploy(deployment Deployment) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.deployment = deployment
	for name, worker := range deployment.Workers {
		if existing, found := e.workerSlots[name]; found {
			existing.setLimit(worker.Concurrency)
		} else {
			e.workerSlots[name] = newLocalSlots(worker.Concurrency)
		}
	}
}

func (e *Engine) current() Deployment {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.deployment
}

type deployedConsumer struct {
	topicName string
	topic     *provider.TopicSpec
	consumer  provider.ConsumerSpec
}

func (d deployedConsumer) isTask() bool { return IsTask(d.topic) }

func (d deployedConsumer) batches() bool { return d.consumer.Batch != nil && d.consumer.Batch.Size > 0 }

func (e *Engine) consumer(topicName, consumerName string) (deployedConsumer, bool) {
	topic, found := e.current().Topics[topicName]
	if !found {
		return deployedConsumer{}, false
	}
	for _, consumer := range topic.Consumers {
		if consumer.Name == consumerName {
			return deployedConsumer{topicName: topicName, topic: topic, consumer: consumer}, true
		}
	}
	return deployedConsumer{}, false
}

func (e *Engine) task(name string) (deployedConsumer, bool) {
	topic, found := e.current().Topics[name]
	if !found || !IsTask(topic) {
		return deployedConsumer{}, false
	}
	return deployedConsumer{topicName: name, topic: topic, consumer: topic.Consumers[0]}, true
}

func (e *Engine) worker(name string) Worker {
	return e.current().Workers[name]
}

func (e *Engine) serves(worker string) bool {
	_, served := e.current().Workers[worker]
	return served
}

func (e *Engine) workerSlotsOf(name string) *localSlots {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.workerSlots[name]
}

func (e *Engine) track(execution string, cancel context.CancelCauseFunc) {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.inFlight[execution] = cancel
}

func (e *Engine) untrack(execution string) {
	e.mu.Lock()
	defer e.mu.Unlock()
	delete(e.inFlight, execution)
}

func (e *Engine) stopInFlight(execution string) {
	e.mu.Lock()
	stop := e.inFlight[execution]
	e.mu.Unlock()
	if stop != nil {
		stop(ErrCanceled)
	}
}
