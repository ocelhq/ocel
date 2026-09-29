package bootstrap

import (
	"bytes"
	"context"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/run"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func bootstrapOf(stacks ...*contractv1.BootstrapStack) *contractv1.BootstrapStatus {
	return &contractv1.BootstrapStatus{
		Tier:    environmentv1.Tier_TIER_PRODUCTION,
		Present: true,
		Stacks:  stacks,
	}
}

func TestPlanBootstrap(t *testing.T) {
	core := &contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, DigestCurrent: true, Required: true}

	tests := []struct {
		name     string
		status   *contractv1.BootstrapStatus
		missing  []string
		stale    []string
		features []string
	}{
		{
			name:   "a bootstrap nothing has been deployed into asks for nothing",
			status: &contractv1.BootstrapStatus{Tier: environmentv1.Tier_TIER_PRODUCTION},
		},
		{
			name: "everything this project needs is there and current",
			status: bootstrapOf(core,
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, DigestCurrent: true, Required: true},
			),
			features: []string{"isr"},
		},
		{
			name: "a required feature that is not there is added",
			status: bootstrapOf(core,
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, DigestCurrent: true, Required: true},
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-image-optimization", Feature: "image-optimization", Required: true},
			),
			missing:  []string{"image-optimization"},
			features: []string{"image-optimization", "isr"},
		},
		{
			name: "a required feature that has fallen behind is refreshed",
			status: bootstrapOf(core,
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, Required: true},
			),
			stale:    []string{"ocel-bootstrap-isr"},
			features: []string{"isr"},
		},
		{
			name: "the core falling behind is a refresh of its own",
			status: bootstrapOf(
				&contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, Required: true},
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, DigestCurrent: true, Required: true},
			),
			stale:    []string{"ocel-bootstrap"},
			features: []string{"isr"},
		},
		{
			name: "a feature no project here needs is neither added nor refreshed",
			status: bootstrapOf(core,
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true},
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-image-optimization", Feature: "image-optimization"},
			),
			features: []string{"isr"},
		},
		{
			name: "one set covers both what is missing and what is behind",
			status: bootstrapOf(core,
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, Required: true},
				&contractv1.BootstrapStack{Name: "ocel-bootstrap-image-optimization", Feature: "image-optimization", Required: true},
			),
			missing:  []string{"image-optimization"},
			stale:    []string{"ocel-bootstrap-isr"},
			features: []string{"image-optimization", "isr"},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			plan := PlanFor(tt.status)
			if !slices.Equal(plan.Missing, tt.missing) {
				t.Errorf("missing = %v, want %v", plan.Missing, tt.missing)
			}
			if !slices.Equal(plan.Stale, tt.stale) {
				t.Errorf("stale = %v, want %v", plan.Stale, tt.stale)
			}
			if !slices.Equal(plan.Features, tt.features) {
				t.Errorf("features = %v, want %v", plan.Features, tt.features)
			}
			if plan.Empty() != (len(tt.missing) == 0 && len(tt.stale) == 0) {
				t.Errorf("empty() = %t for %v/%v", plan.Empty(), plan.Missing, plan.Stale)
			}
		})
	}
}

func TestOfferedBootstrapSendsTheEdgeTheProjectChose(t *testing.T) {
	plan := Plan{Features: []string{"isr"}, Missing: []string{"isr"}}
	front := &contractv1.EdgeSelection{Kind: "relay"}

	req := plan.Request(environmentv1.Tier_TIER_PREVIEW, front)
	if req.GetEdge().GetKind() != "relay" {
		t.Errorf("request edge = %q, want the edge the project chose", req.GetEdge().GetKind())
	}
	if req.GetTier() != environmentv1.Tier_TIER_PREVIEW || !slices.Equal(req.GetFeatures(), plan.Features) {
		t.Errorf("request = %v, want the offered plan's own tier and features", req)
	}
}

func TestOfferBootstrapWithoutATerminal(t *testing.T) {
	core := &contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, DigestCurrent: true, Required: true}

	t.Run("a missing feature stops the run and names the command", func(t *testing.T) {
		status := bootstrapOf(core,
			&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, DigestCurrent: true, Required: true},
			&contractv1.BootstrapStack{Name: "ocel-bootstrap-image-optimization", Feature: "image-optimization", Required: true},
		)
		span, _ := checkSpan(t)
		err := Offer(context.Background(), span, nil, status, environmentv1.Tier_TIER_PRODUCTION, nil, false, io.Discard, nil)
		if err == nil {
			t.Fatal("a deploy against a bootstrap missing a feature it needs was allowed through")
		}
		if !strings.Contains(err.Error(), "ocel bootstrap production --features image-optimization,isr") {
			t.Errorf("refusal = %q, want the literal command to run", err)
		}
	})

	t.Run("a stale stack warns and lets the deploy through", func(t *testing.T) {
		status := bootstrapOf(core,
			&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, Required: true},
		)
		span, seen := checkSpan(t)
		if err := Offer(context.Background(), span, nil, status, environmentv1.Tier_TIER_PREVIEW, nil, false, io.Discard, nil); err != nil {
			t.Fatalf("a bootstrap that is merely behind stopped the deploy: %v", err)
		}
		for _, want := range []string{"⚠ ", "ocel-bootstrap-isr", "ocel bootstrap preview --features isr"} {
			if !strings.Contains(seen.said(), want) {
				t.Errorf("said %q, want a warning containing %q", seen.said(), want)
			}
		}
	})

	t.Run("a bootstrap that includes what this project needs says nothing", func(t *testing.T) {
		status := bootstrapOf(core,
			&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, DigestCurrent: true, Required: true},
		)
		span, seen := checkSpan(t)
		if err := Offer(context.Background(), span, nil, status, environmentv1.Tier_TIER_PRODUCTION, nil, false, io.Discard, nil); err != nil {
			t.Fatalf("offerBootstrap err = %v", err)
		}
		if said := seen.said(); said != "" {
			t.Errorf("said %q, want nothing said about a bootstrap that is what it should be", said)
		}
	})
}

func TestARepairPromptHoldsTheRunWhileItAsks(t *testing.T) {
	core := &contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, DigestCurrent: true, Required: true}
	status := bootstrapOf(core,
		&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, Required: true},
	)
	span, seen := checkSpan(t)
	var out bytes.Buffer

	if err := Offer(context.Background(), span, nil, status, environmentv1.Tier_TIER_PREVIEW, nil, true, &out, strings.NewReader("n\n")); err != nil {
		t.Fatalf("declining to repair a bootstrap that is merely behind stopped the deploy: %v", err)
	}

	got := seen.shape()
	want := []string{"warn", "waiting", "resumed", "warn"}
	if !slices.Equal(got, want) {
		t.Errorf("events = %v, want %v: the question is asked while the run is held, and the advice follows the answer", got, want)
	}
	if !strings.Contains(out.String(), "ocel bootstrap preview --features isr") {
		t.Errorf("asked %q, want the command it offers to run named", out.String())
	}
}

type heard struct {
	mu     sync.Mutex
	events []*streamv1.RunEvent
}

func (h *heard) Receive(ev *streamv1.RunEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, ev)
}

func (h *heard) Close() error { return nil }

func (h *heard) said() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var b strings.Builder
	for _, ev := range h.events {
		if ev.GetBody() != nil {
			continue
		}
		if ev.GetLevel() == progressv1.Level_LEVEL_WARN {
			b.WriteString("⚠ ")
		}
		b.WriteString(ev.GetMessage() + "\n")
	}
	return b.String()
}

func (h *heard) shape() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, ev := range h.events {
		switch {
		case ev.GetWaiting() != nil:
			out = append(out, "waiting")
		case ev.GetResumed() != nil:
			out = append(out, "resumed")
		case ev.GetBody() == nil && ev.GetLevel() == progressv1.Level_LEVEL_WARN:
			out = append(out, "warn")
		case ev.GetBody() == nil:
			out = append(out, "say")
		}
	}
	return out
}

func checkSpan(t *testing.T) (*run.Span, *heard) {
	t.Helper()
	seen := &heard{}
	bus := run.NewBus(time.Now)
	bus.Attach(seen)
	_, run, err := bus.Begin(context.Background(), "ocel deploy", "")
	if err != nil {
		t.Fatal(err)
	}
	return run.Phase(progressv1.Phase_PHASE_CHECK), seen
}
