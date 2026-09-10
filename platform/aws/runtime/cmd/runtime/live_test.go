package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/constants"
	vars "github.com/ocelhq/ocel/platform/aws/provider/vars/live"
)

type sink struct {
	mu    sync.Mutex
	lines []string
}

func (s *sink) Write(p []byte) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.lines = append(s.lines, strings.TrimSuffix(string(p), "\n"))
	return len(p), nil
}

func (s *sink) messages(t *testing.T) []map[string]string {
	t.Helper()
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]map[string]string, 0, len(s.lines))
	for _, line := range s.lines {
		var msg struct {
			Values map[string]string `json:"values"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("the child was pushed %q, which is not a control message: %v", line, err)
		}
		out = append(out, msg.Values)
	}
	return out
}

type stubValues struct {
	env      []string
	bindings []vars.Binding
	failure  error
	released chan struct{}
	pushed   map[string]string

	mu       sync.Mutex
	sink     io.Writer
	refreshs int
}

func (s *stubValues) Prefetch(context.Context) <-chan error {
	done := make(chan error, 1)
	go func() {
		if s.released != nil {
			<-s.released
		}
		done <- s.failure
	}()
	return done
}

func (s *stubValues) Join(done <-chan error) error {
	if done == nil {
		return nil
	}
	return <-done
}

func (s *stubValues) Failure() error { return s.failure }

func (s *stubValues) Abandoned() <-chan struct{} {
	if s.failure == nil {
		return nil
	}
	abandon := make(chan struct{})
	close(abandon)
	return abandon
}

func (s *stubValues) Attach(w io.Writer) {
	s.mu.Lock()
	s.sink = w
	s.mu.Unlock()
	if len(s.pushed) > 0 {
		line, _ := json.Marshal(map[string]any{"type": "liveValues", "values": s.pushed})
		w.Write(append(line, '\n'))
	}
}

func (s *stubValues) Refresh(context.Context) {
	s.mu.Lock()
	s.refreshs++
	s.mu.Unlock()
}

func (s *stubValues) Env() []string { return s.env }

func (s *stubValues) Bindings() []vars.Binding { return s.bindings }

func fakeSpawn(gotBudget *time.Duration) spawner {
	return func(_ []string, budget time.Duration, onControl func(io.Writer), _ <-chan struct{}) (*nodeChild, error) {
		*gotBudget = budget
		if onControl != nil {
			onControl(&sink{})
		}
		return &nodeChild{}, nil
	}
}

func neverReady(_ []string, budget time.Duration, _ func(io.Writer), abandon <-chan struct{}) (*nodeChild, error) {
	select {
	case <-abandon:
	case <-time.After(budget):
	}
	return nil, fmt.Errorf("node did not signal ready within %s", budget)
}

func TestBringUpNode(t *testing.T) {
	t.Run("the spawn runs beside the prefetch rather than behind it", func(t *testing.T) {
		l := &stubValues{released: make(chan struct{}), pushed: map[string]string{"DB_PASSWORD": "hunter2"}}
		out := &sink{}
		prefetch := l.Prefetch(context.Background())

		spawn := func(_ []string, _ time.Duration, onControl func(io.Writer), _ <-chan struct{}) (*nodeChild, error) {
			close(l.released)
			onControl(out)
			return &nodeChild{}, nil
		}

		child, err := bringUpNode(spawn, l, prefetch, nil, time.Minute)
		if err != nil {
			t.Fatalf("bringUpNode: %v", err)
		}
		if child.live != liveValues(l) {
			t.Error("the child was not given the cache the invocations it serves refresh from")
		}
		if msgs := out.messages(t); len(msgs) != 1 || msgs[0]["DB_PASSWORD"] != "hunter2" {
			t.Errorf("messages = %+v, want the prefetched generation delivered to the child", msgs)
		}
	})

	t.Run("drift is reported as itself and not as node never starting", func(t *testing.T) {
		const budget = 5 * time.Second
		l := &stubValues{failure: fmt.Errorf("binding db--main is published as BINDING_TYPE_BUCKET: %w", vars.ErrDrift)}

		_, err := bringUpNode(neverReady, l, l.Prefetch(context.Background()), nil, budget)
		if err == nil {
			t.Fatal("bringUpNode = nil, want init refused")
		}
		if !strings.Contains(err.Error(), "BINDING_TYPE_BUCKET") {
			t.Errorf("error = %v, want the drift named", err)
		}
		if strings.Contains(err.Error(), "did not signal ready") {
			t.Errorf("error = %v, which reports the symptom and buries the cause", err)
		}
	})

	t.Run("a failed prefetch is reported as the store error not as node never starting", func(t *testing.T) {
		const budget = 5 * time.Second
		l := &stubValues{failure: errors.New("AccessDeniedException: dynamodb:Query")}

		start := time.Now()
		_, err := bringUpNode(neverReady, l, l.Prefetch(context.Background()), nil, budget)
		took := time.Since(start)

		if err == nil {
			t.Fatal("bringUpNode = nil, want a function that cannot resolve a value it declared refused")
		}
		if !strings.Contains(err.Error(), "AccessDeniedException") {
			t.Errorf("error = %v, want what the store said: node timing out is the symptom, not the cause", err)
		}
		if strings.Contains(err.Error(), "did not signal ready") {
			t.Errorf("error = %v, which reports the symptom and buries the cause", err)
		}
		if took >= budget {
			t.Errorf("init spent %s of a %s budget waiting on a child that was never going to come up", took, budget)
		}
	})
}

func TestChildEnv(t *testing.T) {
	t.Run("carries the live declaration beside the delivered class", func(t *testing.T) {
		bakedEnv := []string{"OCEL_VAR_STRIPE_KEY=sk_baked"}
		l := &stubValues{env: []string{"OCEL_LIVE_KEYS=DB_PASSWORD"}}

		got := childEnv(bakedEnv, l, nil)

		for _, want := range []string{"OCEL_VAR_STRIPE_KEY=sk_baked", "OCEL_LIVE_KEYS=DB_PASSWORD"} {
			if !slices.Contains(got, want) {
				t.Errorf("childEnv = %q, missing %q", got, want)
			}
		}

		bare := childEnv(bakedEnv, nil, nil)
		if len(bare) != 1 {
			t.Errorf("childEnv for a function with no live values = %q, want only the class delivered in the environment", bare)
		}
	})

	t.Run("hands the child the proxy it must reach and the token that opens it", func(t *testing.T) {
		proxyEnv := []string{
			constants.RuntimeAddressEnvName + "=http://127.0.0.1:41000",
			channel.SessionTokenEnvVar + "=deadbeef",
		}
		l := &stubValues{env: []string{"OCEL_LIVE_KEYS=DB_PASSWORD"}}

		got := childEnv([]string{"OCEL_VAR_STRIPE_KEY=sk_baked"}, l, proxyEnv)

		for _, want := range proxyEnv {
			if !slices.Contains(got, want) {
				t.Errorf("childEnv = %q, missing %q, so the app cannot reach the proxy serving its bindings", got, want)
			}
		}
	})
}
