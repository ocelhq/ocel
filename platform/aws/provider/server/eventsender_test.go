package server

import (
	"context"
	"errors"
	"sync"
	"testing"
	"time"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

type recordingStream struct {
	events []*deploymentsv1.DeployEvent
}

func (r *recordingStream) send(ev *deploymentsv1.DeployEvent) error {
	r.events = append(r.events, ev)
	return nil
}

const (
	concurrentUploaders = 64
	concurrentAppStacks = 4
)

func TestDeployReporterSerializesConcurrentSends(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)
	progress, logf := newDeployReporter(sender)

	var wg sync.WaitGroup
	for i := 0; i < concurrentUploaders; i++ {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			progress(deploymentsv1.Phase_PHASE_UPLOADING, "uploading function artifacts", uint32(i), concurrentUploaders)
		}(i)
	}
	for i := 0; i < concurrentAppStacks; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			logf("app-deploy stack log line")
		}()
	}
	wg.Wait()

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}

	want := concurrentUploaders + concurrentAppStacks
	if got := len(stream.events); got != want {
		t.Fatalf("got %d events, want %d", got, want)
	}
}

func TestDeployReporterParallelMultiAppDeploy(t *testing.T) {
	t.Parallel()

	const apps = concurrentAppStacks
	const functionsPerApp = 16
	const logLinesPerApp = 8

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)
	progress, logf := newDeployReporter(sender)

	var wg sync.WaitGroup
	for a := 0; a < apps; a++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var uploads sync.WaitGroup
			for f := 0; f < functionsPerApp; f++ {
				uploads.Add(1)
				go func(f int) {
					defer uploads.Done()
					progress(deploymentsv1.Phase_PHASE_UPLOADING, "uploading function artifacts", uint32(f), functionsPerApp)
				}(f)
			}
			uploads.Wait()
			for l := 0; l < logLinesPerApp; l++ {
				logf("app-deploy stack log line")
			}
		}()
	}
	wg.Wait()

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}

	want := apps*functionsPerApp + apps*logLinesPerApp
	if got := len(stream.events); got != want {
		t.Fatalf("got %d events, want %d", got, want)
	}
}

func TestEventSenderPropagatesSendError(t *testing.T) {
	t.Parallel()

	wantErr := errors.New("boom")
	var calls int
	sender := newEventSender(context.Background(), func(*deploymentsv1.DeployEvent) error {
		calls++
		if calls == 2 {
			return wantErr
		}
		return nil
	})

	const total = 5
	for i := 0; i < total; i++ {
		sender.send(logEvent("line"))
	}

	if err := sender.close(); !errors.Is(err, wantErr) {
		t.Fatalf("close() error = %v, want %v", err, wantErr)
	}
	if calls != total {
		t.Fatalf("send attempted for %d events, want %d: a mid-stream error must not skip the rest, including the terminal result", calls, total)
	}
}

func TestEventSenderAppliesBackpressureWithoutDroppingEvents(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	var mu sync.Mutex
	sender := newEventSender(context.Background(), func(ev *deploymentsv1.DeployEvent) error {
		mu.Lock()
		defer mu.Unlock()
		return stream.send(ev)
	})

	const total = eventSenderBuffer + 50
	var wg sync.WaitGroup
	for i := 0; i < total; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sender.send(logEvent("line"))
		}()
	}
	wg.Wait()

	if err := sender.close(); err != nil {
		t.Fatalf("close() error = %v", err)
	}
	if got := len(stream.events); got != total {
		t.Fatalf("got %d events, want %d", got, total)
	}
}

func TestEventSenderSendRacingCloseNeverPanics(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	sender := newEventSender(context.Background(), stream.send)

	const concurrent = 50
	var wg sync.WaitGroup
	for i := 0; i < concurrent; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sender.send(logEvent("racing close"))
		}()
	}

	closeDone := make(chan struct{})
	go func() {
		defer close(closeDone)
		sender.close()
	}()

	wg.Wait()
	<-closeDone

	for i := 0; i < concurrent; i++ {
		sender.send(logEvent("after close"))
	}
}

func TestEventSenderSendUnblocksOnContextCancellation(t *testing.T) {
	t.Parallel()

	blockDrain := make(chan struct{})
	ctx, cancel := context.WithCancel(context.Background())

	sender := newEventSender(ctx, func(*deploymentsv1.DeployEvent) error {
		<-blockDrain
		return nil
	})

	var fillers sync.WaitGroup
	for i := 0; i < eventSenderBuffer+1; i++ {
		fillers.Add(1)
		go func() {
			defer fillers.Done()
			sender.send(logEvent("line"))
		}()
	}
	fillers.Wait()

	cancel()

	done := make(chan struct{})
	go func() {
		defer close(done)
		sender.send(logEvent("blocked"))
	}()

	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("send did not observe context cancellation once the buffer was saturated")
	}

	close(blockDrain)
	sender.close()
}
