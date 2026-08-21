package server

import (
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

func TestBootstrapStatus(t *testing.T) {
	t.Parallel()

	deployed := bootstrap.Deployed{
		Present:  true,
		Schema:   bootstrap.RequiredSchema,
		AutoHeal: true,
		Stacks: []bootstrap.StackStamp{
			{Name: "ocel-bootstrap", Present: true, Schema: bootstrap.RequiredSchema, Digest: "same", Intended: "same", WrittenBy: "1.4.0"},
			{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, Schema: bootstrap.RequiredSchema, Digest: "old", Intended: "new", WrittenBy: "1.3.0"},
		},
	}

	t.Run("it carries every stack it read", func(t *testing.T) {
		t.Parallel()

		status := (&Server{writer: "1.4.0"}).bootstrapStatus(deployed, environmentv1.Tier_TIER_PRODUCTION, []string{"isr"})
		if len(status.GetStacks()) != 2 {
			t.Fatalf("described %d stacks, want 2", len(status.GetStacks()))
		}
		if !status.GetStacks()[0].GetDigestCurrent() {
			t.Error("a stack whose digest matches what this build renders reads as stale")
		}
		if status.GetStacks()[1].GetDigestCurrent() {
			t.Error("a stack whose digest moved reads as current")
		}
		if !status.GetAutoHeal() {
			t.Error("the auto-heal the core carries did not reach the report")
		}
		if status.GetRequiredSchema() != bootstrap.RequiredSchema {
			t.Errorf("required schema = %d, want %d", status.GetRequiredSchema(), bootstrap.RequiredSchema)
		}
	})

	t.Run("a newer writer at equal schema reads as a downgrade", func(t *testing.T) {
		t.Parallel()

		if !(&Server{writer: "1.3.9"}).bootstrapStatus(deployed, environmentv1.Tier_TIER_PRODUCTION, []string{"isr"}).GetDowngrade() {
			t.Error("an older build writing over a bootstrap a newer one wrote is not flagged")
		}
		if (&Server{writer: "1.4.0"}).bootstrapStatus(deployed, environmentv1.Tier_TIER_PRODUCTION, []string{"isr"}).GetDowngrade() {
			t.Error("the build that wrote the bootstrap is flagged as downgrading it")
		}
		if (&Server{writer: "dev+cafe"}).bootstrapStatus(deployed, environmentv1.Tier_TIER_PRODUCTION, []string{"isr"}).GetDowngrade() {
			t.Error("a dev build cannot be ordered against a release and must not claim a downgrade")
		}
	})
}

func TestDriftReport(t *testing.T) {
	t.Parallel()

	deployed := bootstrap.Deployed{
		Present: true,
		Stacks: []bootstrap.StackStamp{
			{Name: "ocel-bootstrap", Present: true, Digest: "same", Intended: "same"},
			{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, Digest: "old", Intended: "new"},
			{Name: "ocel-bootstrap-image-optimization", Feature: "image-optimization", Present: true, Digest: "old", Intended: "new"},
		},
	}

	t.Run("it names only the stale stacks this deploy needs", func(t *testing.T) {
		t.Parallel()

		got := driftReport(deployed, []string{"isr"}, "ocel bootstrap")
		if !strings.Contains(got, "ocel-bootstrap-isr") {
			t.Errorf("report = %q, want it to name the stale stack this deploy needs", got)
		}
		if strings.Contains(got, "image-optimization") {
			t.Errorf("report = %q, want it silent about a stale feature this deploy does not need", got)
		}
		if !strings.Contains(got, "`ocel bootstrap`") {
			t.Errorf("report = %q, want it to name what a user runs to fix it", got)
		}
	})

	t.Run("a bootstrap this build wrote reports nothing", func(t *testing.T) {
		t.Parallel()

		current := bootstrap.Deployed{Present: true, Stacks: []bootstrap.StackStamp{
			{Name: "ocel-bootstrap", Present: true, Digest: "same", Intended: "same"},
		}}
		if got := driftReport(current, nil, "ocel bootstrap"); got != "" {
			t.Errorf("report = %q, want nothing", got)
		}
	})
}

func TestHealableStacks(t *testing.T) {
	t.Parallel()

	deployed := bootstrap.Deployed{
		Present:  true,
		AutoHeal: true,
		Stacks: []bootstrap.StackStamp{
			{Name: "ocel-bootstrap", Present: true, Digest: "old", Intended: "new", WrittenBy: "1.4.0"},
			{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, Digest: "old", Intended: "new", WrittenBy: "1.4.0"},
			{Name: "ocel-bootstrap-image-optimization", Feature: "image-optimization", Present: true, Digest: "same", Intended: "same"},
		},
	}

	got := healableStacks(deployed, []string{"isr", "image-optimization"})
	if len(got) != 1 || got[0] != "ocel-bootstrap-isr" {
		t.Errorf("healable stacks = %v, want only the stale feature stack: core never heals and a current one has nothing to do", got)
	}
	if len(healableStacks(deployed, nil)) != 0 {
		t.Error("a stack no required feature names was offered to heal")
	}
	if written := bootstrapWriter(deployed); written != "1.4.0" {
		t.Errorf("bootstrap writer = %q, want the core's", written)
	}
}

func TestSchemaAheadRefusal(t *testing.T) {
	t.Parallel()

	msg := schemaAheadRefusal(bootstrap.RequiredSchema+1, false).Error()
	if !strings.Contains(msg, "ocel bootstrap --destroy") {
		t.Errorf("refusal = %q, want it to name what drops the bootstrap", msg)
	}
	if strings.Contains(msg, "--force") {
		t.Errorf("refusal = %q, want no escape hatch offered", msg)
	}
	if got := strings.Count(msg, "\n"); got != 1 {
		t.Errorf("refusal = %q, want exactly two lines", msg)
	}
}
