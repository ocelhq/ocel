package providerprocess

import (
	"context"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
)

func TestReadLogsHandsEveryResponseToTheCallerInOrder(t *testing.T) {
	t.Parallel()

	ctx, span, _ := deploySpan(t)
	p := startFake(t, ctx, "success", span, Questions{})

	var bodies []string
	err := ReadLogs(ctx, p, &contractv1.ReadLogsRequest{Slug: "acme", Limit: 10}, func(resp *contractv1.ReadLogsResponse) error {
		switch resp.GetBody().(type) {
		case *contractv1.ReadLogsResponse_Batch:
			bodies = append(bodies, "batch")
		case *contractv1.ReadLogsResponse_Notice:
			bodies = append(bodies, "notice")
		}
		return nil
	})
	if err != nil {
		t.Fatalf("ReadLogs() error = %v", err)
	}
	if got := strings.Join(bodies, ","); got != "batch,notice" {
		t.Errorf("responses = %s, want batch,notice", got)
	}
}

func TestReadLogsReportsACancelledReadAsCancelled(t *testing.T) {
	t.Parallel()

	ctx, span, _ := deploySpan(t)
	p := startFake(t, ctx, "hang-logs", span, Questions{})
	called, cancel := context.WithCancel(ctx)
	defer cancel()

	errs := make(chan error, 1)
	first := make(chan struct{}, 1)
	go func() {
		errs <- ReadLogs(called, p, &contractv1.ReadLogsRequest{Slug: "acme", Limit: 10}, func(*contractv1.ReadLogsResponse) error {
			select {
			case first <- struct{}{}:
			default:
			}
			return nil
		})
	}()
	select {
	case <-first:
	case <-time.After(5 * time.Second):
		t.Fatal("never received the first batch")
	}
	cancel()

	select {
	case err := <-errs:
		if !errors.Is(err, context.Canceled) || !strings.Contains(err.Error(), "cancelled") {
			t.Errorf("ReadLogs() error = %v, want a cancellation wrapping context.Canceled", err)
		}
		if strings.Contains(err.Error(), "connection lost") {
			t.Errorf("ReadLogs() error = %v, want a ctrl-C not dressed up as a lost connection", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("ReadLogs() hung after the caller cancelled")
	}
}

func TestReadLogsKeepsTheProvidersRefusalAndDoesNotCallItALostConnection(t *testing.T) {
	t.Parallel()

	ctx, span, _ := deploySpan(t)
	p := startFake(t, ctx, "refuse-logs", span, Questions{})

	err := ReadLogs(ctx, p, &contractv1.ReadLogsRequest{Slug: "acme", Limit: 10}, func(*contractv1.ReadLogsResponse) error { return nil })
	if err == nil || !strings.Contains(err.Error(), "web is not deployed here") {
		t.Fatalf("ReadLogs() error = %v, want the provider's refusal", err)
	}
	if strings.Contains(err.Error(), "connection lost") {
		t.Errorf("ReadLogs() error = %v, want a refusal not reported as a lost connection", err)
	}
}

func TestReadLogsStopsAndReturnsTheCallersError(t *testing.T) {
	t.Parallel()

	ctx, span, _ := deploySpan(t)
	p := startFake(t, ctx, "hang-logs", span, Questions{})
	broken := errors.New("stdout closed")

	err := ReadLogs(ctx, p, &contractv1.ReadLogsRequest{Slug: "acme", Limit: 10}, func(*contractv1.ReadLogsResponse) error { return broken })
	if !errors.Is(err, broken) {
		t.Errorf("ReadLogs() error = %v, want the caller's error", err)
	}
}

func readLogMessages(ctx context.Context, p *Provider) ([]string, error) {
	var messages []string
	err := ReadLogs(ctx, p, &contractv1.ReadLogsRequest{Slug: "acme", Limit: 10}, func(resp *contractv1.ReadLogsResponse) error {
		for _, entry := range resp.GetBatch().GetEntries() {
			messages = append(messages, entry.GetMessage())
		}
		return nil
	})
	return messages, err
}

func TestReadLogsAsksAQuestionTheProviderPutsBeforeAnyLogsAndReadsAgain(t *testing.T) {
	t.Parallel()

	ctx, span, _ := deploySpan(t)
	fake := newQuestionFake(t, "unknown-host-key")
	asker := &scriptedPrompt{attended: true, answer: true}
	p := startFake(t, ctx, "unknown-host-key", span, answering(asker, io.Discard), fake.env()...)

	messages, err := readLogMessages(ctx, p)
	if err != nil {
		t.Fatalf("ReadLogs() error = %v, want the read retried once the key was trusted", err)
	}
	if len(asker.asked) != 1 {
		t.Errorf("asked %d times, want once", len(asker.asked))
	}
	if got := strings.Join(messages, ","); got != "listening" {
		t.Errorf("messages = %s, want the one batch once", got)
	}
	if got := fake.drivenTimes(t); got != 2 {
		t.Errorf("the read ran %d times, want twice: asked, then read", got)
	}
}

func TestReadLogsNeverAsksAQuestionAfterLogsWerePrintedSoNoneArePrintedTwice(t *testing.T) {
	t.Parallel()

	ctx, span, _ := deploySpan(t)
	fake := newQuestionFake(t, "unknown-host-key-after-logs")
	asker := &scriptedPrompt{attended: true, answer: true}
	p := startFake(t, ctx, "unknown-host-key-after-logs", span, answering(asker, io.Discard), fake.env()...)

	messages, err := readLogMessages(ctx, p)
	if err == nil {
		t.Fatal("ReadLogs() error = nil, want the late question reported as an error")
	}
	if len(asker.asked) != 0 {
		t.Errorf("asked %v, want no prompt once logs were printed", asker.asked)
	}
	if got := strings.Join(messages, ","); got != "listening" {
		t.Errorf("messages = %s, want the batch handed over once", got)
	}
	if got := fake.drivenTimes(t); got != 1 {
		t.Errorf("the read ran %d times, want once", got)
	}
}
