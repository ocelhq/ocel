package main

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/aws/runtime/bytecode"
)

type fakeCache struct {
	key    string
	source bytecode.Source
	fail   bool

	mu      sync.Mutex
	uploads int
	flushes int
}

func (c *fakeCache) Key() string { return c.key }

func (c *fakeCache) Source() bytecode.Source {
	if c.source == "" {
		return bytecode.SourceNone
	}
	return c.source
}

func (c *fakeCache) Cached() bool { return c.Source() != bytecode.SourceNone }

func (c *fakeCache) Upload(ctx context.Context, _ time.Time, flush bytecode.Flush) bytecode.Outcome {
	c.mu.Lock()
	c.uploads++
	c.mu.Unlock()
	if flush != nil {
		if _, ok := flush(ctx); ok {
			c.mu.Lock()
			c.flushes++
			c.mu.Unlock()
		}
	}
	if c.fail {
		return bytecode.Outcome{Reason: "access denied"}
	}
	return bytecode.Outcome{Uploaded: true}
}

func (c *fakeCache) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.uploads
}

func TestUploadBytecodeCacheOnce(t *testing.T) {
	t.Run("runs at most once per instance", func(t *testing.T) {
		cache := &fakeCache{}
		m := &nodeChild{cache: cache}

		for range 3 {
			m.uploadBytecodeCacheOnce(context.Background())
		}

		if cache.count() != 1 {
			t.Errorf("uploads = %d, want 1", cache.count())
		}
	})

	t.Run("does not retry after a failure", func(t *testing.T) {
		cache := &fakeCache{fail: true}
		m := &nodeChild{cache: cache}

		m.uploadBytecodeCacheOnce(context.Background())
		m.uploadBytecodeCacheOnce(context.Background())

		if cache.count() != 1 {
			t.Errorf("uploads = %d, want 1", cache.count())
		}
	})

	t.Run("no ops once the cache came from somewhere", func(t *testing.T) {
		cache := &fakeCache{source: bytecode.SourceEmbedded}
		m := &nodeChild{cache: cache}

		m.uploadBytecodeCacheOnce(context.Background())

		if cache.count() != 0 {
			t.Errorf("uploads = %d, want none for a cache this instance did not build", cache.count())
		}
	})

	t.Run("no ops for an unconfigured function", func(t *testing.T) {
		m := &nodeChild{}
		m.uploadBytecodeCacheOnce(context.Background())
	})
}

func TestHandleInvocationBytecode(t *testing.T) {
	t.Run("uploads after completion and before the next next", func(t *testing.T) {
		node := okNode(t)
		rt, _ := fakeRuntime(t, []byte(getEvent))

		goSide, jsSide := net.Pipe()
		t.Cleanup(func() { goSide.Close(); jsSide.Close() })

		cache := &fakeCache{}
		m := &nodeChild{
			upstream:  upstream{port: portOf(t, node), client: &http.Client{}},
			control:   goSide,
			lifecycle: true,
			pending:   map[string]chan struct{}{},
			cache:     cache,
		}
		go m.drainControl(bufio.NewReader(goSide))

		done := make(chan error, 1)
		go func() { done <- handleInvocation(context.Background(), rt, m) }()

		time.Sleep(75 * time.Millisecond)
		if early := cache.count(); early != 0 {
			t.Fatalf("uploads before invocation-complete = %d, want the upload held until the invocation is done", early)
		}

		if _, err := jsSide.Write([]byte(`{"type":"invocation-complete","payload":{"requestId":"req-1"}}` + "\n")); err != nil {
			t.Fatalf("write control message: %v", err)
		}

		select {
		case err := <-done:
			if err != nil {
				t.Fatalf("handleInvocation: %v", err)
			}
		case <-time.After(5 * time.Second):
			t.Fatal("handleInvocation never returned")
		}

		if cache.count() != 1 {
			t.Errorf("uploads = %d, want the cache published before the loop moved on", cache.count())
		}
	})
}

func controlConnPair(t *testing.T) (*nodeChild, *bufio.Reader, net.Conn) {
	t.Helper()
	parentSide, nodeSide := net.Pipe()
	t.Cleanup(func() { parentSide.Close(); nodeSide.Close() })
	m := &nodeChild{control: parentSide, lifecycle: true, pending: map[string]chan struct{}{}}
	go m.drainControl(bufio.NewReader(parentSide))
	return m, bufio.NewReader(nodeSide), nodeSide
}

type flushOutcome struct {
	ack compileCacheFlushedPayload
	ok  bool
}

func startFlush(m *nodeChild, ctx context.Context) <-chan flushOutcome {
	done := make(chan flushOutcome, 1)
	go func() {
		ack, ok := m.flushCompileCache(ctx)
		done <- flushOutcome{ack: ack, ok: ok}
	}()
	return done
}

func TestFlushCompileCache(t *testing.T) {
	t.Run("delivers the ack to the waiter", func(t *testing.T) {
		m, nodeReader, nodeConn := controlConnPair(t)
		done := startFlush(m, context.Background())

		line, err := nodeReader.ReadString('\n')
		if err != nil {
			t.Fatalf("node never received the flush request: %v", err)
		}
		if line != flushCompileCacheLine {
			t.Errorf("node received %q, want %q", line, flushCompileCacheLine)
		}
		fmt.Fprintf(nodeConn, `{"type":"compile-cache-flushed","payload":{"dir":%q,"ok":true}}`+"\n", "/tmp/ocel/compile-cache")

		got := <-done
		if !got.ok {
			t.Fatal("flushCompileCache() ok = false, want the ack delivered")
		}
		if got.ack.Dir != "/tmp/ocel/compile-cache" || !got.ack.OK {
			t.Errorf("ack = %+v, want the dir and ok node reported", got.ack)
		}
	})

	t.Run("carries a null dir as not OK", func(t *testing.T) {
		m, nodeReader, nodeConn := controlConnPair(t)
		done := startFlush(m, context.Background())

		if _, err := nodeReader.ReadString('\n'); err != nil {
			t.Fatalf("node never received the flush request: %v", err)
		}
		fmt.Fprintln(nodeConn, `{"type":"compile-cache-flushed","payload":{"dir":null,"ok":false}}`)

		got := <-done
		if !got.ok {
			t.Fatal("flushCompileCache() ok = false, want the ack delivered")
		}
		if got.ack.Dir != "" || got.ack.OK {
			t.Errorf("ack = %+v, want an empty dir and ok=false", got.ack)
		}
	})

	t.Run("gives up when the child never answers", func(t *testing.T) {
		m, nodeReader, _ := controlConnPair(t)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		start := time.Now()
		done := startFlush(m, ctx)
		if _, err := nodeReader.ReadString('\n'); err != nil {
			t.Fatalf("node never received the flush request: %v", err)
		}

		if got := <-done; got.ok {
			t.Error("flushCompileCache() ok = true, want a give-up")
		}
		if elapsed := time.Since(start); elapsed > compileCacheFlushTimeout {
			t.Errorf("took %s, want the context to end the wait early", elapsed)
		}
	})

	t.Run("gives up when the child never reads the request", func(t *testing.T) {
		m, _, _ := controlConnPair(t)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		start := time.Now()
		if _, ok := m.flushCompileCache(ctx); ok {
			t.Error("flushCompileCache() ok = true, want a give-up")
		}
		if elapsed := time.Since(start); elapsed > compileCacheFlushTimeout {
			t.Errorf("took %s, want the write deadline to end the attempt", elapsed)
		}
	})

	t.Run("clears the write deadline afterwards", func(t *testing.T) {
		m, nodeReader, _ := controlConnPair(t)
		ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
		defer cancel()

		done := startFlush(m, ctx)
		if _, err := nodeReader.ReadString('\n'); err != nil {
			t.Fatalf("node never received the flush request: %v", err)
		}
		<-done

		time.Sleep(100 * time.Millisecond)
		written := make(chan error, 1)
		go func() {
			_, err := m.control.Write([]byte("{\"type\":\"liveValues\"}\n"))
			written <- err
		}()
		if _, err := nodeReader.ReadString('\n'); err != nil {
			t.Fatalf("a later push never arrived: %v", err)
		}
		if err := <-written; err != nil {
			t.Errorf("a later push failed on the flush's stale deadline: %v", err)
		}
	})

	t.Run("no ops without a control connection", func(t *testing.T) {
		m := &nodeChild{}
		if _, ok := m.flushCompileCache(context.Background()); ok {
			t.Error("flushCompileCache() ok = true, want false with no child attached")
		}
	})
}

func TestDrainControl(t *testing.T) {
	t.Run("drops an unawaited flush ack", func(t *testing.T) {
		m, _, nodeConn := controlConnPair(t)
		waiter := m.beginInvocation("req-1")

		fmt.Fprintln(nodeConn, `{"type":"compile-cache-flushed","payload":{"dir":"/tmp/x","ok":true}}`)
		fmt.Fprintln(nodeConn, `{"type":"invocation-complete","payload":{"requestId":"req-1"}}`)

		select {
		case <-waiter:
		case <-time.After(2 * time.Second):
			t.Fatal("drain loop stalled on an ack with no waiter")
		}
	})
}

func TestNodeChildEnv(t *testing.T) {
	t.Run("carries the compile cache only when gated", func(t *testing.T) {
		t.Run("gate open", func(t *testing.T) {
			t.Setenv("OCEL_BYTECODE_PREFIX", "ocel")
			want := bytecode.Env()
			if len(want) != 1 {
				t.Fatalf("bytecode.Env() = %v, want exactly one entry with the gate open", want)
			}

			env := nodeChildEnv("/tmp/ocel-control.sock", []string{"OCEL_BAKED_X=1"})
			if !slices.Contains(env, want[0]) {
				t.Errorf("child env has no %q: %v", want[0], env)
			}
			if !slices.Contains(env, "OCEL_CONTROL_SOCKET=/tmp/ocel-control.sock") {
				t.Error("child env lost the control socket")
			}
			if !slices.Contains(env, "OCEL_BAKED_X=1") {
				t.Error("child env lost the caller's extra entries")
			}
		})

		t.Run("gate closed", func(t *testing.T) {
			t.Setenv("OCEL_BYTECODE_PREFIX", "")
			for _, e := range nodeChildEnv("/tmp/ocel-control.sock", nil) {
				if strings.HasPrefix(e, "NODE_COMPILE_CACHE=") {
					t.Errorf("child env has %q with the gate closed", e)
				}
			}
		})
	})
}

func TestBringUpNodeWithBytecode(t *testing.T) {
	primes := func(cache compileCache, cost time.Duration) primer {
		return func(context.Context) compileCache {
			time.Sleep(cost)
			return cache
		}
	}

	t.Run("priming cost is carved out of the startup budget", func(t *testing.T) {
		const primeCost = 200 * time.Millisecond
		l := &stubValues{}

		var gotBudget time.Duration
		child, err := bringUpNodeWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, nil, nil, time.Now(), primes(&fakeCache{key: "k"}, primeCost))
		if err != nil {
			t.Fatalf("bringUpNodeWithBytecode: %v", err)
		}
		if child.cache == nil {
			t.Fatal("cache = nil, want the primed cache attached")
		}
		if gotBudget > startupBudget-primeCost/2 {
			t.Errorf("budget handed to bringUpNode = %s, want it reduced by priming's %s cost", gotBudget, primeCost)
		}
	})

	t.Run("floors the spawn budget", func(t *testing.T) {
		l := &stubValues{}
		start := time.Now().Add(-2 * startupBudget)

		var gotBudget time.Duration
		if _, err := bringUpNodeWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, nil, nil, start, primes(&fakeCache{}, 0)); err != nil {
			t.Fatalf("bringUpNodeWithBytecode: %v", err)
		}
		if gotBudget != minSpawnBudget {
			t.Errorf("budget handed to bringUpNode = %s, want the floor of %s", gotBudget, minSpawnBudget)
		}
	})

	t.Run("normal carve out is unaffected by the floor", func(t *testing.T) {
		l := &stubValues{}

		var gotBudget time.Duration
		if _, err := bringUpNodeWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, nil, nil, time.Now(), primes(&fakeCache{}, 0)); err != nil {
			t.Fatalf("bringUpNodeWithBytecode: %v", err)
		}
		if gotBudget <= minSpawnBudget {
			t.Errorf("budget handed to bringUpNode = %s, want it well above the floor for a start this recent", gotBudget)
		}
		if gotBudget > startupBudget {
			t.Errorf("budget handed to bringUpNode = %s, want it at most startupBudget", gotBudget)
		}
	})

	t.Run("an unconfigured deployment carries no cache", func(t *testing.T) {
		l := &stubValues{}

		var gotBudget time.Duration
		child, err := bringUpNodeWithBytecode(context.Background(), fakeSpawn(&gotBudget), l, nil, nil, time.Now(), nil)
		if err != nil {
			t.Fatalf("bringUpNodeWithBytecode: %v", err)
		}
		if child.cache != nil {
			t.Error("cache != nil, want none for a deployment that primed nothing")
		}
		if child.cached() {
			t.Error("cached() = true, want a deployment with no cache to report none")
		}
	})
}
