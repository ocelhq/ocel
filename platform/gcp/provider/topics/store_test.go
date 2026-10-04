package topics_test

import (
	"crypto/sha256"
	"encoding/hex"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
	"github.com/ocelhq/ocel/platform/gcp/provider/topics"
)

func scopeOf(t *testing.T) topics.Scope {
	t.Helper()
	sum := sha256.Sum256([]byte(t.Name() + time.Now().String()))
	return topics.Scope{Slug: "shop", Tier: environment.TierPreview, Environment: "t" + hex.EncodeToString(sum[:6])}
}

func TestATaskDatabaseIsNamedForItsNamespaceAndTier(t *testing.T) {
	t.Parallel()

	if got := ports.TaskDatabase("ocel", environment.TierProduction); got != "ocel-production-tasks" {
		t.Errorf("TaskDatabase(ocel, production) = %q, want ocel-production-tasks", got)
	}
}
