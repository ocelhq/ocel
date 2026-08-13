package server

import (
	"errors"
	"sync"
	"testing"

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
	sender := newEventSender(stream.send)
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
	sender := newEventSender(stream.send)
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
	sender := newEventSender(func(*deploymentsv1.DeployEvent) error {
		calls++
		if calls == 2 {
			return wantErr
		}
		return nil
	})

	for i := 0; i < 5; i++ {
		sender.send(logEvent("line"))
	}

	if err := sender.close(); !errors.Is(err, wantErr) {
		t.Fatalf("close() error = %v, want %v", err, wantErr)
	}
}

func TestEventSenderAppliesBackpressureWithoutDroppingEvents(t *testing.T) {
	t.Parallel()

	stream := &recordingStream{}
	var mu sync.Mutex
	sender := newEventSender(func(ev *deploymentsv1.DeployEvent) error {
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
	sender := newEventSender(stream.send)

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
