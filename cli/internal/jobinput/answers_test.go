package jobinput_test

import (
	"context"
	"errors"
	"fmt"
	"io"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/cli/internal/jobinput"
	streamv1 "github.com/ocelhq/ocel/pkg/proto/cli/stream/v1"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
)

type holds struct {
	mu      sync.Mutex
	waiting []*streamv1.WaitingEvent
	resumed []string
	asked   chan *cloudv1.JobInput
}

func newHolds() *holds { return &holds{asked: make(chan *cloudv1.JobInput, 4)} }

func (h *holds) Hold(waiting *streamv1.WaitingEvent) func(string) {
	h.mu.Lock()
	h.waiting = append(h.waiting, waiting)
	h.mu.Unlock()
	h.asked <- waiting.GetInput()
	return func(reason string) {
		h.mu.Lock()
		defer h.mu.Unlock()
		h.resumed = append(h.resumed, reason)
	}
}

func (h *holds) next(t *testing.T) *cloudv1.JobInput {
	t.Helper()
	select {
	case input := <-h.asked:
		return input
	case <-time.After(5 * time.Second):
		t.Fatal("no waiting event was held")
		return nil
	}
}

func answering(t *testing.T, h *holds, stdin *io.PipeWriter, line func(id string) string) {
	t.Helper()
	go func() {
		input := h.next(t)
		_, _ = io.WriteString(stdin, line(input.GetId())+"\n")
	}()
}

func TestAConfirmationIsHeldAsAJobInputAndAnsweredByTheLineNamingItsID(t *testing.T) {
	t.Parallel()

	for _, confirmed := range []bool{true, false} {
		t.Run(fmt.Sprint(confirmed), func(t *testing.T) {
			t.Parallel()
			stdin, write := io.Pipe()
			h := newHolds()
			answering(t, h, write, func(id string) string {
				return `{"inputId":"stale_1","confirmed":true}` + "\n" + fmt.Sprintf(`{"inputId":%q,"confirmed":%t}`, id, confirmed)
			})

			got, err := jobinput.New(stdin).Confirm(context.Background(), h, "This will create a NEW project.", "Continue?")
			if err != nil {
				t.Fatalf("Confirm() error = %v", err)
			}
			if got != confirmed {
				t.Errorf("Confirm() = %t, want %t: the answer to an older input is not this one's", got, confirmed)
			}
			asked := h.waiting[0].GetInput()
			if asked.GetId() == "" || asked.GetConfirmation().GetQuestion() != "Continue?" || asked.GetConfirmation().GetFinding() != "This will create a NEW project." {
				t.Errorf("held input = %v, want an id, the finding and the question", asked)
			}
			if len(h.resumed) != 1 {
				t.Errorf("resumed %d times, want once the answer came", len(h.resumed))
			}
		})
	}
}

func TestMissingVariablesAreHeldAsAJobInputAndResolvedByTheLineNamingItsID(t *testing.T) {
	t.Parallel()

	stdin, write := io.Pipe()
	h := newHolds()
	answering(t, h, write, func(id string) string { return fmt.Sprintf(`{"input_id":%q,"resolved":true}`, id) })

	needed := &cloudv1.VariablesNeeded{Tier: environmentv1.Tier_TIER_PRODUCTION, Variables: []*cloudv1.NeededVariable{{Key: "DATABASE_URL", Secret: true}}}
	missing := &streamv1.MissingVariables{Cells: []*streamv1.MissingVariable{{Key: "DATABASE_URL", Reason: "no value"}}}
	resolved, err := jobinput.New(stdin).Resolve(context.Background(), h, needed, missing)
	if err != nil || !resolved {
		t.Fatalf("Resolve() = %t, %v, want resolved", resolved, err)
	}
	held := h.waiting[0]
	if held.GetInput().GetVariables().GetVariables()[0].GetKey() != "DATABASE_URL" || held.GetMissing().GetCells()[0].GetKey() != "DATABASE_URL" {
		t.Errorf("held %v, want the variables needed as the job's input and as the missing cells", held)
	}
}

func TestEachInputGetsItsOwnIDAndOneAnswerPerLine(t *testing.T) {
	t.Parallel()

	stdin, write := io.Pipe()
	h := newHolds()
	answers := jobinput.New(stdin)
	go func() {
		for range 2 {
			input := h.next(t)
			_, _ = fmt.Fprintf(write, "{\"inputId\":%q,\"confirmed\":true}\n", input.GetId())
		}
	}()
	for range 2 {
		if ok, err := answers.Confirm(context.Background(), h, "", "Continue?"); err != nil || !ok {
			t.Fatalf("Confirm() = %t, %v", ok, err)
		}
	}
	if first, second := h.waiting[0].GetInput().GetId(), h.waiting[1].GetInput().GetId(); first == second {
		t.Errorf("both inputs are %q, want an id each so an answer names the one it answers", first)
	}
}

func TestAnAnswerThatIsNotOneFailsTheAsk(t *testing.T) {
	t.Parallel()

	for name, line := range map[string]func(id string) string{
		"not JSON":                  func(string) string { return "yes" },
		"no answer":                 func(id string) string { return fmt.Sprintf(`{"inputId":%q}`, id) },
		"an answer of another kind": func(id string) string { return fmt.Sprintf(`{"inputId":%q,"resolved":true}`, id) },
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			stdin, write := io.Pipe()
			h := newHolds()
			answering(t, h, write, line)
			if _, err := jobinput.New(stdin).Confirm(context.Background(), h, "", "Continue?"); err == nil {
				t.Error("Confirm() error = nil, want the answer refused")
			}
		})
	}
}

func TestStdinEndingBeforeTheAnswerFailsTheAsk(t *testing.T) {
	t.Parallel()

	h := newHolds()
	_, err := jobinput.New(strings.NewReader("")).Confirm(context.Background(), h, "", "Continue?")
	if err == nil || !strings.Contains(err.Error(), "stdin") {
		t.Errorf("Confirm() error = %v, want one saying stdin ended before the answer", err)
	}
}

func TestACanceledRunStopsWaitingForTheAnswer(t *testing.T) {
	t.Parallel()

	stdin, _ := io.Pipe()
	ctx, cancel := context.WithCancel(context.Background())
	h := newHolds()
	go func() {
		h.next(t)
		cancel()
	}()
	if _, err := jobinput.New(stdin).Confirm(ctx, h, "", "Continue?"); !errors.Is(err, context.Canceled) {
		t.Errorf("Confirm() error = %v, want context.Canceled", err)
	}
}

func TestAnAskThatFailsStillResumesTheHeldJob(t *testing.T) {
	t.Parallel()

	for name, fail := range map[string]func(h *holds) (io.Reader, context.Context){
		"an answer that is not one": func(h *holds) (io.Reader, context.Context) {
			stdin, write := io.Pipe()
			answering(t, h, write, func(string) string { return "yes" })
			return stdin, context.Background()
		},
		"stdin ending": func(*holds) (io.Reader, context.Context) {
			return strings.NewReader(""), context.Background()
		},
		"a canceled run": func(h *holds) (io.Reader, context.Context) {
			stdin, _ := io.Pipe()
			ctx, cancel := context.WithCancel(context.Background())
			go func() {
				h.next(t)
				cancel()
			}()
			return stdin, ctx
		},
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			h := newHolds()
			stdin, ctx := fail(h)
			if _, err := jobinput.New(stdin).Confirm(ctx, h, "", "Continue?"); err == nil {
				t.Fatal("Confirm() error = nil, want the ask failed")
			}
			h.mu.Lock()
			defer h.mu.Unlock()
			if len(h.resumed) != 1 {
				t.Errorf("resumed %d times, want once, so the job leaves waiting", len(h.resumed))
			}
		})
	}
}
