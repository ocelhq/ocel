package live

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/keyvalue"
	"github.com/ocelhq/ocel/pkg/seal"
)

const (
	QueueDatabaseName = "ocel"
	QueueSecretName   = "queue-password"

	QueueDeliverySecretName = "delivery-secret"

	queueDatabaseEntry = "database"
	queueTopicsEntry   = "topics"
	queueWorkersEntry  = "workers"
)

type QueueDatabase struct {
	Container      string `json:"container"`
	Stack          string `json:"stack"`
	Sealed         string `json:"sealed"`
	DeliverySealed string `json:"deliverySealed"`
}

type QueueWorker struct {
	App         string `json:"app"`
	Stack       string `json:"stack"`
	Container   string `json:"container"`
	Concurrency int    `json:"concurrency,omitempty"`
}

type Queue struct {
	Tier     environment.Tier
	Project  string
	Env      string
	Database QueueDatabase
	Topics   map[string]json.RawMessage
	Workers  map[string]QueueWorker
}

func QueuePartition(tier environment.Tier) keyvalue.Partition {
	return keyvalue.Partition{Tier: tier, Root: keyvalue.RootQueues}
}

func QueueDatabaseKey(tier environment.Tier, project, env string) keyvalue.Key {
	return QueuePartition(tier).Key(project, env, queueDatabaseEntry)
}

func QueueTopicKey(tier environment.Tier, project, env, topic string) keyvalue.Key {
	return QueuePartition(tier).Key(project, env, queueTopicsEntry, topic)
}

func QueueWorkerKey(tier environment.Tier, project, env, worker string) keyvalue.Key {
	return QueuePartition(tier).Key(project, env, queueWorkersEntry, worker)
}

func QueueTopicsUnder(tier environment.Tier, project, env string) (keyvalue.Partition, []string) {
	return QueuePartition(tier), []string{project, env, queueTopicsEntry}
}

func QueueWorkersUnder(tier environment.Tier, project, env string) (keyvalue.Partition, []string) {
	return QueuePartition(tier), []string{project, env, queueWorkersEntry}
}

func NewQueueSecretAssociatedData(project string, tier environment.Tier, stack string) (seal.AssociatedData, error) {
	return NewSecretAssociatedData(project, tier, stack, StoreSecretFolder, QueueDatabaseName, QueueSecretName)
}

func NewQueueDeliverySecretAssociatedData(project string, tier environment.Tier, stack string) (seal.AssociatedData, error) {
	return NewSecretAssociatedData(project, tier, stack, StoreSecretFolder, QueueDatabaseName, QueueDeliverySecretName)
}

type queueKey struct{ project, env string }

func ReadQueues(ctx context.Context, store keyvalue.Store, tier environment.Tier) ([]Queue, error) {
	entries, err := store.List(ctx, QueuePartition(tier))
	if err != nil {
		return nil, err
	}
	found := map[queueKey]*Queue{}
	var order []queueKey
	queueOf := func(key queueKey) *Queue {
		queue, seen := found[key]
		if !seen {
			queue = &Queue{Tier: tier, Project: key.project, Env: key.env, Topics: map[string]json.RawMessage{}, Workers: map[string]QueueWorker{}}
			found[key] = queue
			order = append(order, key)
		}
		return queue
	}
	databases := map[queueKey]bool{}
	for _, entry := range entries {
		path := entry.Key.Path
		if len(path) < 3 {
			continue
		}
		key := queueKey{project: path[0], env: path[1]}
		switch {
		case len(path) == 3 && path[2] == queueDatabaseEntry:
			queue := queueOf(key)
			if err := json.Unmarshal(entry.Value, &queue.Database); err != nil {
				return nil, fmt.Errorf("read %s: %w", entry.Key, err)
			}
			databases[key] = true
		case len(path) == 4 && path[2] == queueTopicsEntry:
			queueOf(key).Topics[path[3]] = entry.Value
		case len(path) == 4 && path[2] == queueWorkersEntry:
			var worker QueueWorker
			if err := json.Unmarshal(entry.Value, &worker); err != nil {
				return nil, fmt.Errorf("read %s: %w", entry.Key, err)
			}
			queueOf(key).Workers[path[3]] = worker
		}
	}
	queues := make([]Queue, 0, len(databases))
	for _, key := range order {
		if databases[key] {
			queues = append(queues, *found[key])
		}
	}
	return queues, nil
}
