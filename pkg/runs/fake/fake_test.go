package fake_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/pkg/runs/fake"
	"github.com/ocelhq/ocel/pkg/runs/runsconformance"
)

func TestTheFakeStoreKeepsRunsAndRecordsAsEveryRunStoreMust(t *testing.T) {
	t.Parallel()
	runsconformance.Store(t, func(*testing.T) runs.Store { return fake.NewStore() })
}

func TestTheEngineOverTheFakeQueueKeepsEveryRunSemantic(t *testing.T) {
	t.Parallel()
	runsconformance.Engine(t, func(_ *testing.T, topics map[string]*provider.TopicSpec) runsconformance.Wiring {
		queue := fake.NewQueue(topics)
		return runsconformance.Wiring{Store: fake.NewStore(), Queue: queue, Receiver: queue}
	})
}
