package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/ocelhq/ocel/platform/aws/runtime/bytecode"
)

const warmEvent = `{"ocel":{"warm":1}}`

const warmedReply = `{"type":"compile-cache-warmed","payload":` +
	`{"ok":true,"state":"warmed","entries":42,"loaded":41,` +
	`"failures":[{"entry":"app/broken/page.js","message":"boom"}],` +
	`"stoppedBy":"complete","bytes":1234,"dir":"/tmp/ocel/compile-cache"}}`

const unsupportedReply = `{"type":"compile-cache-warmed","payload":{"ok":false,"state":"unsupported","dir":null}}`

const stoppedReply = `{"type":"compile-cache-warmed","payload":` +
	`{"ok":true,"state":"warmed","entries":42,"loaded":33,"failures":[],` +
	`"stoppedBy":"ceiling","skipped":["app/a/page","app/b/page"],"skippedCount":9,` +
	`"bytes":1234,"dir":"/tmp/ocel/compile-cache"}}`

func TestIsWarmInvocation(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		want    bool
	}{
		{name: "the warm payload", payload: warmEvent, want: true},
		{name: "a real function url event", payload: getEvent},
		{name: "the warm object smuggled in a body", payload: `{"version":"2.0","rawPath":"/","body":"{\"ocel\":{\"warm\":1}}"}`},
		{name: "the warm object smuggled in a header", payload: `{"version":"2.0","headers":{"ocel":"{\"warm\":1}"}}`},
		{name: "an ocel object with no warm", payload: `{"ocel":{}}`},
		{name: "warm switched off", payload: `{"ocel":{"warm":0}}`},
		{name: "not json at all", payload: `not json`},
		{name: "empty payload", payload: ``},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := isWarmInvocation([]byte(tc.payload)); got != tc.want {
				t.Errorf("isWarmInvocation(%s) = %v, want %v", tc.payload, got, tc.want)
			}
		})
	}
}

func warmRuntime(t *testing.T, event []byte, deadline time.Time) (*runtimeClient, *capturedResponse) {
	t.Helper()
	captured := &capturedResponse{}
	mux := http.NewServeMux()
	mux.HandleFunc("/"+runtimeAPIVersion+"/runtime/invocation/next", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Lambda-Runtime-Aws-Request-Id", "req-1")
		w.Header().Set("Lambda-Runtime-Invoked-Function-Arn", "arn:aws:lambda:us-east-1:123:function:fn")
		w.Header().Set("Lambda-Runtime-Deadline-Ms", strconv.FormatInt(deadline.UnixMilli(), 10))
		w.Write(event)
	})
	mux.HandleFunc("/"+runtimeAPIVersion+"/runtime/invocation/req-1/response", func(w http.ResponseWriter, r *http.Request) {
		body, _ := io.ReadAll(r.Body)
		captured.body = body
		w.WriteHeader(http.StatusAccepted)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return newRuntimeClient(strings.TrimPrefix(srv.URL, "http://")), captured
}

const warmKey = "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz"

type scriptedCache struct {
	key     string
	source  bytecode.Source
	outcome bytecode.Outcome
	delay   time.Duration

	mu      sync.Mutex
	uploads int
}

func (c *scriptedCache) Key() string { return c.key }

func (c *scriptedCache) Source() bytecode.Source {
	if c.source == "" {
		return bytecode.SourceNone
	}
	return c.source
}

func (c *scriptedCache) Cached() bool { return c.Source() != bytecode.SourceNone }

func (c *scriptedCache) Upload(context.Context, time.Time, bytecode.Flush) bytecode.Outcome {
	c.mu.Lock()
	c.uploads++
	c.mu.Unlock()
	time.Sleep(c.delay)
	return c.outcome
}

func (c *scriptedCache) count() int {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.uploads
}

func publishes() *scriptedCache {
	return &scriptedCache{key: warmKey, outcome: bytecode.Outcome{Uploaded: true, Bytes: 520}}
}

func warmFixture(t *testing.T, cache *scriptedCache, reply string) *nodeChild {
	t.Helper()
	m, nodeReader, nodeConn := controlConnPair(t)
	m.cache = cache
	go func() {
		if _, err := nodeReader.ReadString('\n'); err != nil || reply == "" {
			return
		}
		fmt.Fprintln(nodeConn, reply)
	}()
	return m
}

func warmCtx(t *testing.T, remaining time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(remaining))
	t.Cleanup(cancel)
	return ctx
}

func TestWarmBytecodeCache(t *testing.T) {
	t.Run("publishes inline and reports it", func(t *testing.T) {
		cache := publishes()
		m := warmFixture(t, cache, warmedReply)

		got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

		if got.State != warmStatePublished {
			t.Fatalf("state = %q (%+v), want %q", got.State, got, warmStatePublished)
		}
		if got.Uploaded == nil || !*got.Uploaded {
			t.Errorf("uploaded = %v, want true", got.Uploaded)
		}
		if got.Entries != 42 || got.Loaded != 41 || got.StoppedBy != "complete" {
			t.Errorf("summary = %+v, want node's own entries, loaded and stoppedBy", got)
		}
		if len(got.Failures) != 1 || got.Failures[0].Entry != "app/broken/page.js" {
			t.Errorf("failures = %+v, want the one node reported", got.Failures)
		}
		if got.Key != warmKey {
			t.Errorf("key = %q, want the cache's %q", got.Key, warmKey)
		}
		if got.Bytes <= 0 {
			t.Errorf("bytes = %d, want what was measured for the ceiling", got.Bytes)
		}
		if cache.count() != 1 {
			t.Fatalf("uploads = %d, want the cache published inline", cache.count())
		}
	})

	t.Run("spends the instances one upload", func(t *testing.T) {
		cache := publishes()
		m := warmFixture(t, cache, warmedReply)

		ctx := warmCtx(t, 10*time.Second)
		if got := m.warmBytecodeCache(ctx); got.State != warmStatePublished {
			t.Fatalf("state = %q, want %q", got.State, warmStatePublished)
		}
		m.uploadBytecodeCacheOnce(ctx)

		if cache.count() != 1 {
			t.Errorf("uploads = %d, want the post-invocation path to see the work as already spent", cache.count())
		}
	})

	t.Run("already cached answers without touching the child", func(t *testing.T) {
		cache := &scriptedCache{key: warmKey, source: bytecode.SourceS3}
		m := warmFixture(t, cache, warmedReply)

		start := time.Now()
		got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

		if got.State != warmStateAlreadyCached {
			t.Fatalf("state = %q, want %q", got.State, warmStateAlreadyCached)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("took %s, want an immediate answer", elapsed)
		}
		if cache.count() != 0 {
			t.Errorf("uploads = %d, want nothing published for a cache that came from somewhere", cache.count())
		}
		if got.Key != warmKey {
			t.Errorf("key = %q, want the cache's %q", got.Key, warmKey)
		}
		if got.Source != bytecode.SourceS3 {
			t.Errorf("source = %q, want %q", got.Source, bytecode.SourceS3)
		}
	})

	t.Run("already cached names which leg served it", func(t *testing.T) {
		m := warmFixture(t, &scriptedCache{key: warmKey, source: bytecode.SourceEmbedded}, warmedReply)

		got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

		if got.State != warmStateAlreadyCached {
			t.Fatalf("state = %q, want %q", got.State, warmStateAlreadyCached)
		}
		if got.Source != bytecode.SourceEmbedded {
			t.Errorf("source = %q, want %q", got.Source, bytecode.SourceEmbedded)
		}
	})

	t.Run("a pass that publishes reports no source", func(t *testing.T) {
		m := warmFixture(t, publishes(), warmedReply)

		got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

		if got.State != warmStatePublished {
			t.Fatalf("state = %q, want %q", got.State, warmStatePublished)
		}
		if got.Source != bytecode.SourceNone {
			t.Errorf("source = %q, want %q", got.Source, bytecode.SourceNone)
		}
	})

	t.Run("disabled for an unconfigured deployment", func(t *testing.T) {
		m := &nodeChild{}

		got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

		if got.State != warmStateDisabled {
			t.Errorf("state = %q, want %q", got.State, warmStateDisabled)
		}
	})

	t.Run("unsupported artifact still publishes what init loaded", func(t *testing.T) {
		cache := publishes()
		m := warmFixture(t, cache, unsupportedReply)

		got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

		if got.State != warmStatePublished {
			t.Fatalf("state = %q (%+v), want %q", got.State, got, warmStatePublished)
		}
		if cache.count() != 1 {
			t.Fatalf("uploads = %d, want the cache INIT produced published anyway", cache.count())
		}
		if !strings.Contains(got.Uncounted, "no compile-cache warm capability") {
			t.Errorf("uncounted = %q, want the counts reported unknown with the reason", got.Uncounted)
		}
		if got.Entries != 0 || got.Loaded != 0 {
			t.Errorf("summary = %+v, want no counts it never measured", got)
		}
	})

	t.Run("publishes when node never reports back", func(t *testing.T) {
		cache := publishes()
		m := warmFixture(t, cache, "")

		got := m.warmBytecodeCache(warmCtx(t, 3*time.Second))

		if got.State != warmStatePublished {
			t.Fatalf("state = %q (%+v), want %q", got.State, got, warmStatePublished)
		}
		if cache.count() != 1 {
			t.Errorf("uploads = %d, want whatever was loaded published anyway", cache.count())
		}
		if !strings.Contains(got.Uncounted, "did not report back") {
			t.Errorf("uncounted = %q, want the counts reported unknown with the reason", got.Uncounted)
		}
	})

	t.Run("collects a late report", func(t *testing.T) {
		cache := publishes()
		cache.delay = 400 * time.Millisecond
		m, nodeReader, nodeConn := controlConnPair(t)
		m.cache = cache

		go func() {
			if _, err := nodeReader.ReadString('\n'); err != nil {
				return
			}
			time.Sleep(600 * time.Millisecond)
			fmt.Fprintln(nodeConn, warmedReply)
		}()

		got := m.warmBytecodeCache(warmCtx(t, bytecode.UploadBudget+completionMargin+500*time.Millisecond))

		if got.State != warmStatePublished {
			t.Fatalf("state = %q (%+v), want %q", got.State, got, warmStatePublished)
		}
		if got.Entries != 42 || got.Loaded != 41 {
			t.Errorf("summary = %+v, want the late report's own counts", got)
		}
		if got.Uncounted != "" {
			t.Errorf("uncounted = %q, want the counts accounted for after all", got.Uncounted)
		}
	})

	t.Run("carries the skipped entries", func(t *testing.T) {
		m := warmFixture(t, publishes(), stoppedReply)

		got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

		if got.StoppedBy != "ceiling" || got.SkippedCount != 9 {
			t.Fatalf("summary = %+v, want the walk's own stop and skipped count", got)
		}
		if len(got.Skipped) != 2 || got.Skipped[0] != "app/a/page" {
			t.Errorf("skipped = %v, want the names node reported", got.Skipped)
		}
	})

	t.Run("reports a failed upload as failed", func(t *testing.T) {
		m := warmFixture(t, &scriptedCache{key: warmKey, outcome: bytecode.Outcome{Reason: "could not upload the compile cache to " + warmKey + ": access denied"}}, warmedReply)

		got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

		if got.State != warmStateFailed {
			t.Fatalf("state = %q, want %q", got.State, warmStateFailed)
		}
		if got.Uploaded == nil || *got.Uploaded {
			t.Errorf("uploaded = %v, want false", got.Uploaded)
		}
		if !strings.Contains(got.Error, "access denied") {
			t.Errorf("error = %q, want it to carry what the store said", got.Error)
		}
	})

	t.Run("reports a cache over the ceiling", func(t *testing.T) {
		over := int64(bytecode.CacheCeiling) + 1
		m := warmFixture(t, &scriptedCache{key: warmKey, outcome: bytecode.Outcome{
			Bytes:  over,
			Reason: fmt.Sprintf("compile cache is %d bytes, over the %d byte ceiling; skipping upload", over, bytecode.CacheCeiling),
		}}, warmedReply)

		got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

		if got.State != warmStateFailed {
			t.Fatalf("state = %q, want %q", got.State, warmStateFailed)
		}
		if !strings.Contains(got.Error, "ceiling") {
			t.Errorf("error = %q, want the ceiling named", got.Error)
		}
		if got.Uploaded == nil || *got.Uploaded {
			t.Errorf("uploaded = %v, want false over the ceiling", got.Uploaded)
		}
	})

	t.Run("an object already there reads as already cached", func(t *testing.T) {
		m := warmFixture(t, &scriptedCache{key: warmKey, outcome: bytecode.Outcome{Existed: true}}, warmedReply)

		got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

		if got.State != warmStateAlreadyCached {
			t.Fatalf("state = %q, want %q", got.State, warmStateAlreadyCached)
		}
		if got.Uploaded != nil {
			t.Errorf("uploaded = %v, want nothing reported once the object exists", *got.Uploaded)
		}
	})

	t.Run("stops waiting at the load deadline", func(t *testing.T) {
		m := warmFixture(t, publishes(), "")

		start := time.Now()
		m.warmBytecodeCache(warmCtx(t, bytecode.UploadBudget+completionMargin+500*time.Millisecond))

		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("took %s, want the wait ended at the load deadline", elapsed)
		}
	})

	t.Run("fails without asking when no window is left", func(t *testing.T) {
		cache := publishes()
		m := warmFixture(t, cache, warmedReply)

		got := m.warmBytecodeCache(warmCtx(t, bytecode.UploadBudget))

		if got.State != warmStateFailed {
			t.Fatalf("state = %q, want %q", got.State, warmStateFailed)
		}
		if cache.count() != 0 {
			t.Errorf("uploads = %d, want the pass abandoned before any work", cache.count())
		}
	})
}

func TestWarmLoadDeadline(t *testing.T) {
	t.Run("reserves the publish leg and the margin", func(t *testing.T) {
		deadline := time.Now().Add(30 * time.Second)
		ctx, cancel := context.WithDeadline(context.Background(), deadline)
		defer cancel()

		got, ok := warmLoadDeadline(ctx)
		if !ok {
			t.Fatal("warmLoadDeadline() ok = false, want a window")
		}
		want := deadline.Add(-bytecode.UploadBudget - completionMargin)
		if got.Sub(want) > time.Millisecond || want.Sub(got) > time.Millisecond {
			t.Errorf("load deadline = %s, want %s", got, want)
		}
	})

	t.Run("a deadline with nothing left to reserve yields no window", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(bytecode.UploadBudget))
		defer cancel()

		if got, ok := warmLoadDeadline(ctx); ok {
			t.Errorf("warmLoadDeadline() = %s, true, want no window", got)
		}
	})

	t.Run("no deadline falls back to the assumed invocation budget", func(t *testing.T) {
		got, ok := warmLoadDeadline(context.Background())
		if !ok {
			t.Fatal("warmLoadDeadline() ok = false, want a window")
		}
		if remaining := time.Until(got); remaining <= 0 || remaining > warmInvocationBudget {
			t.Errorf("load window = %s, want a positive window under %s", remaining, warmInvocationBudget)
		}
	})
}

func TestWarmCompileCache(t *testing.T) {
	t.Run("request carries the deadline and the ceiling", func(t *testing.T) {
		m, nodeReader, nodeConn := controlConnPair(t)
		deadline := time.Now().Add(5 * time.Second)

		done := make(chan struct{})
		go func() {
			defer close(done)
			m.warmCompileCache(context.Background(), deadline)
			m.endWarmExchange()
		}()

		line, err := nodeReader.ReadString('\n')
		if err != nil {
			t.Fatalf("node never received the warm request: %v", err)
		}
		var msg struct {
			Type    string `json:"type"`
			Payload struct {
				DeadlineMs   int64 `json:"deadlineMs"`
				CeilingBytes int64 `json:"ceilingBytes"`
			} `json:"payload"`
		}
		if err := json.Unmarshal([]byte(line), &msg); err != nil {
			t.Fatalf("warm request is not the expected JSON (%q): %v", line, err)
		}
		if msg.Type != "warm-compile-cache" {
			t.Errorf("type = %q, want warm-compile-cache", msg.Type)
		}
		if msg.Payload.CeilingBytes != bytecode.CacheCeiling {
			t.Errorf("ceilingBytes = %d, want %d", msg.Payload.CeilingBytes, bytecode.CacheCeiling)
		}
		want := deadline.Add(-warmReplyMargin).UnixMilli()
		if msg.Payload.DeadlineMs != want {
			t.Errorf("deadlineMs = %d, want %d (the load deadline less the reply margin)", msg.Payload.DeadlineMs, want)
		}

		fmt.Fprintln(nodeConn, warmedReply)
		<-done
	})

	t.Run("no ops without a control connection", func(t *testing.T) {
		m := &nodeChild{}
		if _, _, ok := m.warmCompileCache(context.Background(), time.Now().Add(time.Second)); ok {
			t.Error("warmCompileCache() ok = true, want false with no child attached")
		}
	})
}

func TestDrainControlWarm(t *testing.T) {
	t.Run("drops an unawaited warm reply", func(t *testing.T) {
		m, _, nodeConn := controlConnPair(t)
		waiter := m.beginInvocation("req-1")

		fmt.Fprintln(nodeConn, warmedReply)
		fmt.Fprintln(nodeConn, `{"type":"invocation-complete","payload":{"requestId":"req-1"}}`)

		select {
		case <-waiter:
		case <-time.After(2 * time.Second):
			t.Fatal("drain loop stalled on a warm reply with no waiter")
		}
	})
}

func TestHandleInvocationWarm(t *testing.T) {
	t.Run("warm payload is answered without forwarding", func(t *testing.T) {
		var hits int
		node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			io.WriteString(w, "ok")
		}))
		defer node.Close()

		cache := publishes()
		m := warmFixture(t, cache, warmedReply)
		m.upstream = upstream{port: portOf(t, node), client: newLoopbackClient()}

		rt, captured := warmRuntime(t, []byte(warmEvent), time.Now().Add(10*time.Second))
		if err := handleInvocation(context.Background(), rt, m); err != nil {
			t.Fatalf("handleInvocation: %v", err)
		}

		if hits != 0 {
			t.Errorf("node served %d requests, want a warm invocation never forwarded", hits)
		}
		if bytes.Contains(captured.body, make([]byte, preludeSeparatorLen)) {
			t.Errorf("answer = %q, want raw JSON rather than prelude framing", captured.body)
		}
		var got warmSummary
		if err := json.Unmarshal(captured.body, &got); err != nil {
			t.Fatalf("answer is not a warm summary (%q): %v", captured.body, err)
		}
		if got.State != warmStatePublished {
			t.Errorf("state = %q, want %q", got.State, warmStatePublished)
		}
		if cache.count() != 1 {
			t.Errorf("uploads = %d, want the cache published during the warm invocation", cache.count())
		}
	})

	t.Run("real event is still forwarded", func(t *testing.T) {
		var hits int
		node := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			hits++
			io.WriteString(w, "ok")
		}))
		defer node.Close()

		rt, captured := fakeRuntime(t, []byte(getEvent))
		m := &nodeChild{upstream: upstream{port: portOf(t, node), client: newLoopbackClient()}}

		if err := handleInvocation(context.Background(), rt, m); err != nil {
			t.Fatalf("handleInvocation: %v", err)
		}

		if hits != 1 {
			t.Errorf("node served %d requests, want the event forwarded once", hits)
		}
		p, body := splitPrelude(t, captured.body)
		if p.StatusCode != http.StatusOK || string(body) != "ok" {
			t.Errorf("response = %d %q, want the app's own 200 ok", p.StatusCode, body)
		}
	})

	t.Run("a warm failure still answers the invocation", func(t *testing.T) {
		parentSide, nodeSide := net.Pipe()
		nodeSide.Close()
		m := &nodeChild{control: parentSide, pending: map[string]chan struct{}{}, cache: &scriptedCache{key: warmKey, outcome: bytecode.Outcome{Reason: "access denied"}}}
		go m.drainControl(bufio.NewReader(parentSide))

		rt, captured := warmRuntime(t, []byte(warmEvent), time.Now().Add(10*time.Second))
		if err := handleInvocation(context.Background(), rt, m); err != nil {
			t.Fatalf("handleInvocation: %v", err)
		}

		var got warmSummary
		if err := json.Unmarshal(captured.body, &got); err != nil {
			t.Fatalf("answer is not a warm summary (%q): %v", captured.body, err)
		}
		if got.State != warmStateFailed {
			t.Errorf("state = %q, want %q", got.State, warmStateFailed)
		}
	})
}

func TestWarmSummary(t *testing.T) {
	t.Run("omits what does not apply", func(t *testing.T) {
		for _, s := range []warmSummary{{State: warmStateAlreadyCached, Source: bytecode.SourceNone}, {State: warmStateDisabled, Source: bytecode.SourceNone}} {
			encoded, err := json.Marshal(s)
			if err != nil {
				t.Fatal(err)
			}
			want := `{"state":"` + s.State + `","source":"none"}`
			if string(encoded) != want {
				t.Errorf("summary = %s, want %s", encoded, want)
			}
		}
	})
}
