package queues

import (
	"encoding/json"
	"fmt"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/statedir"
)

const FilePath = statedir.Name + "/queues.json"

type Manifest struct {
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

func Render(m Manifest) ([]byte, error) {
	if len(m.Topics) == 0 {
		return nil, nil
	}
	if m.Table == "" || m.KeyPrefix == "" {
		return nil, fmt.Errorf("the queue manifest names %d topics and tasks but no table or key prefix to keep their runs under", len(m.Topics))
	}
	return json.Marshal(m)
}

func Parse(data []byte) (Manifest, error) {
	var m Manifest
	if err := json.Unmarshal(data, &m); err != nil {
		return Manifest{}, fmt.Errorf("decode the queue manifest: %w", err)
	}
	return m, nil
}
