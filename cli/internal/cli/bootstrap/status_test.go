package bootstrap

import (
	"bytes"
	"strings"
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func statusOf(tier environmentv1.Tier, stacks ...*contractv1.BootstrapStack) *contractv1.BootstrapStatus {
	return &contractv1.BootstrapStatus{
		Tier:           tier,
		Present:        true,
		Schema:         1,
		RequiredSchema: 1,
		Writer:         "1.4.0",
		Stacks:         stacks,
	}
}

func TestRenderBootstrapStatus(t *testing.T) {
	t.Run("it reports every column for every stack", func(t *testing.T) {
		status := statusOf(environmentv1.Tier_TIER_PRODUCTION,
			&contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, Schema: 1, DigestCurrent: true, WrittenBy: "1.4.0"},
			&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, Schema: 1, WrittenBy: "1.3.0"},
		)
		status.AutoHeal = true

		var out bytes.Buffer
		renderStatus(&out, status)

		for _, want := range []string{"production", "ocel-bootstrap-isr", "isr", "ok", "stale", "1.3.0", "on"} {
			if !strings.Contains(out.String(), want) {
				t.Errorf("report missing %q; got:\n%s", want, out.String())
			}
		}
	})

	t.Run("an absent bootstrap says so and points at the fix", func(t *testing.T) {
		var out bytes.Buffer
		renderStatus(&out, &contractv1.BootstrapStatus{Tier: environmentv1.Tier_TIER_PREVIEW, RequiredSchema: 1})

		if got := out.String(); got != "preview: not bootstrapped — run `ocel bootstrap preview` to set it up\n" {
			t.Errorf("report = %q, want the one line that says it is not there and how to set it up", got)
		}
	})

	t.Run("it names the side that has to move", func(t *testing.T) {
		behind := statusOf(environmentv1.Tier_TIER_PRODUCTION,
			&contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, DigestCurrent: true})
		behind.Schema, behind.RequiredSchema = 1, 2
		ahead := statusOf(environmentv1.Tier_TIER_PRODUCTION,
			&contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, DigestCurrent: true})
		ahead.Schema, ahead.RequiredSchema = 3, 2

		if got := statusProblem(behind); !strings.Contains(got, "ocel bootstrap production") {
			t.Errorf("problem = %q, want it to point at bootstrap", got)
		}
		if got := statusProblem(ahead); !strings.Contains(got, "upgrade the Ocel CLI") {
			t.Errorf("problem = %q, want it to point at the CLI", got)
		}
	})

	t.Run("an apply that never finished is bannered instead of claimed about", func(t *testing.T) {
		status := statusOf(environmentv1.Tier_TIER_PRODUCTION,
			&contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, Schema: 1, DigestCurrent: true, WrittenBy: "1.4.0"})
		status.Unfinished = true

		var out bytes.Buffer
		renderStatus(&out, status)

		if !strings.Contains(out.String(), "never finished") {
			t.Errorf("report = %q, want a half-applied bootstrap bannered as one", out.String())
		}
		if !strings.Contains(out.String(), "ocel bootstrap production") {
			t.Errorf("report = %q, want the command that finishes it", out.String())
		}
		for _, claim := range []string{"STACK", "ok", "1.4.0"} {
			if strings.Contains(out.String(), claim) {
				t.Errorf("report = %q, want %q suppressed: a half-applied host makes no per-item claims", out.String(), claim)
			}
		}
	})
}

func TestBootstrapCheck(t *testing.T) {
	current := statusOf(environmentv1.Tier_TIER_PRODUCTION,
		&contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, Schema: 1, DigestCurrent: true})
	stale := statusOf(environmentv1.Tier_TIER_PREVIEW,
		&contractv1.BootstrapStack{Name: "ocel-bootstrap-preview", Present: true, Schema: 1})
	behind := statusOf(environmentv1.Tier_TIER_PRODUCTION,
		&contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, Schema: 1, DigestCurrent: true})
	behind.RequiredSchema = 2
	half := statusOf(environmentv1.Tier_TIER_PRODUCTION,
		&contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, Schema: 1, DigestCurrent: true})
	half.Unfinished = true

	for _, tc := range []struct {
		name     string
		statuses []*contractv1.BootstrapStatus
		wantErr  bool
	}{
		{"a bootstrap this build wrote passes", []*contractv1.BootstrapStatus{current}, false},
		{"an absent bootstrap passes", []*contractv1.BootstrapStatus{{Tier: environmentv1.Tier_TIER_PREVIEW, RequiredSchema: 1}}, false},
		{"stale content fails", []*contractv1.BootstrapStatus{current, stale}, true},
		{"an older schema fails", []*contractv1.BootstrapStatus{behind}, true},
		{"an apply that never finished fails", []*contractv1.BootstrapStatus{half}, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			err := check(tc.statuses)
			if tc.wantErr && err == nil {
				t.Fatal("bootstrapCheck passed a bootstrap the CLI cannot use as it stands")
			}
			if !tc.wantErr && err != nil {
				t.Fatalf("bootstrapCheck = %v, want nil", err)
			}
		})
	}
}
