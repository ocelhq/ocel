package pgmq

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/taskstoretest"
)

func anEngine(t *testing.T) *Engine {
	t.Helper()
	engine, err := Open(context.Background(), aDatabase(t))
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(engine.Close)
	return engine
}

func TestTheEngineStoreKeepsRunsAndRecordsAsEveryTaskStoreMust(t *testing.T) {
	taskstoretest.Run(t, func(t *testing.T) provider.TaskStore { return anEngine(t).Store() })
}
