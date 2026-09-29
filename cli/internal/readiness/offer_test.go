package readiness

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

func TestARepairOfferWithoutATerminalRefusesWhatIsMissingAndWarnsWhatIsStale(t *testing.T) {
	core := &contractv1.BootstrapStack{Name: "ocel-bootstrap", Present: true, DigestCurrent: true, Required: true}

	t.Run("a missing feature stops the run and names the command", func(t *testing.T) {
		status := bootstrapOf(core,
			&contractv1.BootstrapStack{Name: "ocel-bootstrap-isr", Feature: "isr", Present: true, DigestCurrent: true, Required: true},
			&contractv1.BootstrapStack{Name: "ocel-bootstrap-image-optimization", Feature: "image-optimization", Required: true},
		)
		span, _ := checkSpan(t)
		err := OfferRepair(context.Background(), span, nil, NewGap(status), environmentv1.Tier_TIER_PRODUCTION, nil, false, io.Discard, nil)
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
		if err := OfferRepair(context.Background(), span, nil, NewGap(status), environmentv1.Tier_TIER_PREVIEW, nil, false, io.Discard, nil); err != nil {
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
		if err := OfferRepair(context.Background(), span, nil, NewGap(status), environmentv1.Tier_TIER_PRODUCTION, nil, false, io.Discard, nil); err != nil {
			t.Fatalf("OfferRepair err = %v", err)
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

	if err := OfferRepair(context.Background(), span, nil, NewGap(status), environmentv1.Tier_TIER_PREVIEW, nil, true, &out, strings.NewReader("n\n")); err != nil {
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

func (h *heard) Receive(event *streamv1.RunEvent) {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.events = append(h.events, event)
}

func (h *heard) Close() error { return nil }

func (h *heard) said() string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var b strings.Builder
	for _, event := range h.events {
		if event.GetOperation().GetBody() != nil || event.GetCli() != nil {
			continue
		}
		if event.GetOperation().GetLevel() == progressv1.Level_LEVEL_WARN {
			b.WriteString("⚠ ")
		}
		b.WriteString(event.GetOperation().GetMessage() + "\n")
	}
	return b.String()
}

func (h *heard) shape() []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, event := range h.events {
		switch {
		case event.GetWaiting() != nil:
			out = append(out, "waiting")
		case event.GetResumed() != nil:
			out = append(out, "resumed")
		case event.GetOperation().GetBody() == nil && event.GetCli() == nil && event.GetOperation().GetLevel() == progressv1.Level_LEVEL_WARN:
			out = append(out, "warn")
		case event.GetOperation().GetBody() == nil && event.GetCli() == nil:
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
