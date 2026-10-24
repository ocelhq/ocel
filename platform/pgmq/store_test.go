//go:build integration

package pgmq

import (
	"context"
	"testing"

	"github.com/ocelhq/ocel/pkg/runs"
	"github.com/ocelhq/ocel/pkg/runs/runsconformance"
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

func TestTheEngineStoreKeepsRunsAndRecordsAsEveryRunStoreMust(t *testing.T) {
	runsconformance.Store(t, func(t *testing.T) runs.Store { return anEngine(t).Store() })
}
