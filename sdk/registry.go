package ocel

import (
	"context"
	"fmt"
	"sync"

	topicv1 "ocel.dev/internal/proto/app/topic/v1"
)

type routeKey struct {
	topic    string
	consumer string
}

type route struct {
	kind    RunKind
	name    string
	worker  string
	batched bool
	serve   serveFunc
}

type serveFunc func(ctx context.Context, envelope *topicv1.Envelope, wrap middleware) answer

type middleware func(context.Context, func(context.Context) error) error

var (
	registryMutex     sync.Mutex
	routes            = map[routeKey]*route{}
	workers           = map[string]*WorkerDefinition{}
	undeclaredWorkers = map[string]*WorkerDefinition{}
)

func registerRoute(topic, consumer string, r *route) {
	registryMutex.Lock()
	defer registryMutex.Unlock()
	key := routeKey{topic: topic, consumer: consumer}
	if _, taken := routes[key]; taken && !discovering() {
		panic(fmt.Sprintf("ocel: consumer %q of topic %q is declared twice, and a consumer is declared once", consumer, topic))
	}
	routes[key] = r
}

func registerWorker(worker *WorkerDefinition) {
	registryMutex.Lock()
	defer registryMutex.Unlock()
	if _, taken := workers[worker.name]; taken && !discovering() {
		panic(fmt.Sprintf("ocel: worker %q is declared twice, and a worker is declared once", worker.name))
	}
	workers[worker.name] = worker
}

func findRoute(topic, consumer string) *route {
	registryMutex.Lock()
	defer registryMutex.Unlock()
	return routes[routeKey{topic: topic, consumer: consumer}]
}

func ensureWorker(name string) *WorkerDefinition {
	registryMutex.Lock()
	defer registryMutex.Unlock()
	if worker, declared := workers[name]; declared {
		return worker
	}
	worker, ok := undeclaredWorkers[name]
	if !ok {
		worker = newWorker(name, workerSettings{})
		undeclaredWorkers[name] = worker
	}
	return worker
}

func resolveWorkerName(name string) string {
	if name == "" {
		return defaultWorker
	}
	return name
}
