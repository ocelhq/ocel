package queues

import (
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/statedir"
)

const (
	FilePath = statedir.Name + "/queues.json"

	WorkerConcurrencyEnv = "OCEL_WORKER_CONCURRENCY"

	MaxReceiveCount = 1000
)

type Topology struct {
	Table     string            `json:"table"`
	KeyPrefix string            `json:"keyPrefix"`
	Topics    map[string]Topic  `json:"topics"`
	Workers   map[string]Worker `json:"workers,omitempty"`
}

type Topic struct {
	Declared *provider.TopicSpec `json:"declared"`
	SNS      string              `json:"sns,omitempty"`
	Queues   map[string]string   `json:"queues"`
}

type Worker struct {
	Concurrency int `json:"concurrency,omitempty"`
}

func Render(m Topology) ([]byte, error) {
	if len(m.Topics) == 0 {
		return nil, nil
	}
	if m.Table == "" || m.KeyPrefix == "" {
		return nil, fmt.Errorf("the queue topology names %d topics and tasks but no table or key prefix to keep their runs under", len(m.Topics))
	}
	return json.Marshal(m)
}

func Parse(data []byte) (Topology, error) {
	var m Topology
	if err := json.Unmarshal(data, &m); err != nil {
		return Topology{}, fmt.Errorf("decode the queue topology: %w", err)
	}
	return m, nil
}
