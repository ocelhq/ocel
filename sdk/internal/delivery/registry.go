package delivery

import (
	"context"
	"fmt"
	"sync"

	"ocel.dev/internal/binding"
	topicv1 "ocel.dev/internal/proto/app/topic/v1"
	"ocel.dev/runs"
)

const DefaultWorker = "worker"

type routeKey struct {
	topic    string
	consumer string
}

type Route struct {
	Kind    runs.Kind
	Name    string
	Worker  string
	Batched bool
	Serve   ServeFunc
}

type ServeFunc func(ctx context.Context, envelope *topicv1.Envelope, wrap Middleware) answer

type Middleware func(context.Context, func(context.Context) error) error

var (
	registryMutex     sync.Mutex
	routes            = map[routeKey]*Route{}
	workers           = map[string]*worker{}
	undeclaredWorkers = map[string]*worker{}
)

func RegisterRoute(topic, consumer string, r *Route) {
	registryMutex.Lock()
	defer registryMutex.Unlock()
	key := routeKey{topic: topic, consumer: consumer}
	if _, taken := routes[key]; taken && !binding.Discovering() {
		panic(fmt.Sprintf("ocel: consumer %q of topic %q is declared twice, and a consumer is declared once", consumer, topic))
	}
	routes[key] = r
}

func RegisterWorker(name string, settings WorkerSettings) {
	registryMutex.Lock()
	defer registryMutex.Unlock()
	if _, taken := workers[name]; taken && !binding.Discovering() {
		panic(fmt.Sprintf("ocel: worker %q is declared twice, and a worker is declared once", name))
	}
	workers[name] = newWorker(name, settings)
}

func findRoute(topic, consumer string) *Route {
	registryMutex.Lock()
	defer registryMutex.Unlock()
	return routes[routeKey{topic: topic, consumer: consumer}]
}

func ensureWorker(name string) *worker {
	registryMutex.Lock()
	defer registryMutex.Unlock()
	if declared, ok := workers[name]; ok {
		return declared
	}
	undeclared, ok := undeclaredWorkers[name]
	if !ok {
		undeclared = newWorker(name, WorkerSettings{})
		undeclaredWorkers[name] = undeclared
	}
	return undeclared
}

func ResolveWorkerName(name string) string {
	if name == "" {
		return DefaultWorker
	}
	return name
}
