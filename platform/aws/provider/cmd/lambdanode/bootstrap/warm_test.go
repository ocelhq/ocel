package main

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"
)

const warmEvent = `{"ocel":{"warm":1}}`

const warmedReply = `{"type":"compile-cache-warmed","payload":` +
	`{"ok":true,"state":"warmed","entries":42,"loaded":41,` +
	`"failures":[{"entry":"app/broken/page.js","message":"boom"}],` +
	`"stoppedBy":"complete","bytes":1234,"dir":"/tmp/.ocel/compile-cache"}}`

const unsupportedReply = `{"type":"compile-cache-warmed","payload":{"ok":false,"state":"unsupported","dir":null}}`

const stoppedReply = `{"type":"compile-cache-warmed","payload":` +
	`{"ok":true,"state":"warmed","entries":42,"loaded":33,"failures":[],` +
	`"stoppedBy":"ceiling","skipped":["app/a/page","app/b/page"],"skippedCount":9,` +
	`"bytes":1234,"dir":"/tmp/.ocel/compile-cache"}}`

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

func warmFixture(t *testing.T, store bytecodeStore, dir, reply string) *Membrane {
	t.Helper()
	m, nodeReader, nodeConn := controlConnPair(t)
	u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: dir, OK: true}, true)
	m.bytecode = u
	go func() {
		if _, err := nodeReader.ReadString('\n'); err != nil || reply == "" {
			return
		}
		fmt.Fprintln(nodeConn, reply)
	}()
	return m
}

type slowBytecodeStore struct {
	*fakeBytecodeStore
	delay time.Duration
}

func (s slowBytecodeStore) putObject(ctx context.Context, bucket, key string, body []byte) error {
	time.Sleep(s.delay)
	return s.fakeBytecodeStore.putObject(ctx, bucket, key, body)
}

func warmCtx(t *testing.T, remaining time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(remaining))
	t.Cleanup(cancel)
	return ctx
}

func TestWarmBytecodeCache(t *testing.T) {
	t.Run("publishes inline and reports it", func(t *testing.T) {
		store := &fakeBytecodeStore{}
		m := warmFixture(t, store, cacheDirWith(t, "compiled bytes"), warmedReply)

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
		if got.Key != m.bytecode.key {
			t.Errorf("key = %q, want the resolution's %q", got.Key, m.bytecode.key)
		}
		if got.Bytes <= 0 {
			t.Errorf("bytes = %d, want what was measured for the ceiling", got.Bytes)
		}
		if len(store.puts) != 1 {
			t.Fatalf("puts = %d, want the cache published inline", len(store.puts))
		}
	})

	t.Run("spends the instances one upload", func(t *testing.T) {
		store := &fakeBytecodeStore{}
		m := warmFixture(t, store, cacheDirWith(t, "compiled bytes"), warmedReply)

		ctx := warmCtx(t, 10*time.Second)
		if got := m.warmBytecodeCache(ctx); got.State != warmStatePublished {
			t.Fatalf("state = %q, want %q", got.State, warmStatePublished)
		}
		m.uploadBytecodeCacheOnce(ctx)

		if len(store.puts) != 1 {
			t.Errorf("puts = %d, want the post-invocation path to see the work as already spent", len(store.puts))
		}
	})

	t.Run("already cached answers without touching the child", func(t *testing.T) {
		store := &fakeBytecodeStore{}
		m := warmFixture(t, store, cacheDirWith(t, "compiled bytes"), warmedReply)
		m.bytecode = nil
		m.bytecodeSource = bytecodeSourceS3
		m.bytecodeKey = "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz"

		start := time.Now()
		got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

		if got.State != warmStateAlreadyCached {
			t.Fatalf("state = %q, want %q", got.State, warmStateAlreadyCached)
		}
		if elapsed := time.Since(start); elapsed > time.Second {
			t.Errorf("took %s, want an immediate answer", elapsed)
		}
		if len(store.heads) != 0 || len(store.puts) != 0 {
			t.Errorf("touched S3 (heads=%v puts=%v), want nothing", store.heads, store.puts)
		}
		if got.Key != m.bytecodeKey {
			t.Errorf("key = %q, want the resolution's %q", got.Key, m.bytecodeKey)
		}
		if got.Source != bytecodeSourceS3 {
			t.Errorf("source = %q, want %q", got.Source, bytecodeSourceS3)
		}
	})

	t.Run("already cached names which leg served it", func(t *testing.T) {
		m := warmFixture(t, &fakeBytecodeStore{}, cacheDirWith(t, "compiled bytes"), warmedReply)
		m.bytecode = nil
		m.bytecodeSource = bytecodeSourceEmbedded
		m.bytecodeKey = "ocel/bytecode/my-app/node24.3.1-arm64.tar.gz"

		got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

		if got.State != warmStateAlreadyCached {
			t.Fatalf("state = %q, want %q", got.State, warmStateAlreadyCached)
		}
		if got.Source != bytecodeSourceEmbedded {
			t.Errorf("source = %q, want %q", got.Source, bytecodeSourceEmbedded)
		}
	})

	t.Run("a pass that publishes reports no source", func(t *testing.T) {
		m := warmFixture(t, &fakeBytecodeStore{}, cacheDirWith(t, "compiled bytes"), warmedReply)

		got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

		if got.State != warmStatePublished {
			t.Fatalf("state = %q, want %q", got.State, warmStatePublished)
		}
		if got.Source != bytecodeSourceNone {
			t.Errorf("source = %q, want %q", got.Source, bytecodeSourceNone)
		}
	})

	t.Run("disabled for an unconfigured deployment", func(t *testing.T) {
		m := &Membrane{}

		got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

		if got.State != warmStateDisabled {
			t.Errorf("state = %q, want %q", got.State, warmStateDisabled)
		}
	})

	t.Run("unsupported artifact still publishes what init loaded", func(t *testing.T) {
		store := &fakeBytecodeStore{}
		m := warmFixture(t, store, cacheDirWith(t, "compiled bytes"), unsupportedReply)

		got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

		if got.State != warmStatePublished {
			t.Fatalf("state = %q (%+v), want %q", got.State, got, warmStatePublished)
		}
		if len(store.puts) != 1 {
			t.Fatalf("puts = %d, want the cache INIT produced published anyway", len(store.puts))
		}
		if !strings.Contains(got.Uncounted, "no compile-cache warm capability") {
			t.Errorf("uncounted = %q, want the counts reported unknown with the reason", got.Uncounted)
		}
		if got.Entries != 0 || got.Loaded != 0 {
			t.Errorf("summary = %+v, want no counts it never measured", got)
		}
	})

	t.Run("publishes when node never reports back", func(t *testing.T) {
		store := &fakeBytecodeStore{}
		m := warmFixture(t, store, cacheDirWith(t, "compiled bytes"), "")

		got := m.warmBytecodeCache(warmCtx(t, 3*time.Second))

		if got.State != warmStatePublished {
			t.Fatalf("state = %q (%+v), want %q", got.State, got, warmStatePublished)
		}
		if len(store.puts) != 1 {
			t.Errorf("puts = %d, want whatever was loaded published anyway", len(store.puts))
		}
		if !strings.Contains(got.Uncounted, "did not report back") {
			t.Errorf("uncounted = %q, want the counts reported unknown with the reason", got.Uncounted)
		}
	})

	t.Run("collects a late report", func(t *testing.T) {
		store := slowBytecodeStore{fakeBytecodeStore: &fakeBytecodeStore{}, delay: 400 * time.Millisecond}
		m, nodeReader, nodeConn := controlConnPair(t)
		u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: cacheDirWith(t, "compiled bytes"), OK: true}, true)
		m.bytecode = u

		go func() {
			if _, err := nodeReader.ReadString('\n'); err != nil {
				return
			}
			time.Sleep(600 * time.Millisecond)
			fmt.Fprintln(nodeConn, warmedReply)
		}()

		got := m.warmBytecodeCache(warmCtx(t, bytecodeUploadBudget+completionMargin+500*time.Millisecond))

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
		store := &fakeBytecodeStore{}
		m := warmFixture(t, store, cacheDirWith(t, "compiled bytes"), stoppedReply)

		got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

		if got.StoppedBy != "ceiling" || got.SkippedCount != 9 {
			t.Fatalf("summary = %+v, want the walk's own stop and skipped count", got)
		}
		if len(got.Skipped) != 2 || got.Skipped[0] != "app/a/page" {
			t.Errorf("skipped = %v, want the names node reported", got.Skipped)
		}
	})

	t.Run("reports a failed upload as failed", func(t *testing.T) {
		store := &fakeBytecodeStore{putErr: errors.New("access denied")}
		m := warmFixture(t, store, cacheDirWith(t, "compiled bytes"), warmedReply)

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
		dir := t.TempDir()
		f, err := os.Create(filepath.Join(dir, "big.blob"))
		if err != nil {
			t.Fatal(err)
		}
		if err := f.Truncate(bytecodeCacheCeiling + 1); err != nil {
			t.Fatal(err)
		}
		f.Close()

		store := &fakeBytecodeStore{}
		m := warmFixture(t, store, dir, warmedReply)

		got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

		if got.State != warmStateFailed {
			t.Fatalf("state = %q, want %q", got.State, warmStateFailed)
		}
		if !strings.Contains(got.Error, "ceiling") {
			t.Errorf("error = %q, want the ceiling named", got.Error)
		}
		if len(store.puts) != 0 {
			t.Errorf("puts = %v, want nothing over the ceiling", store.puts)
		}
	})

	t.Run("an object already there reads as already cached", func(t *testing.T) {
		store := &fakeBytecodeStore{exists: true}
		m := warmFixture(t, store, cacheDirWith(t, "compiled bytes"), warmedReply)

		got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

		if got.State != warmStateAlreadyCached {
			t.Fatalf("state = %q, want %q", got.State, warmStateAlreadyCached)
		}
		if len(store.puts) != 0 {
			t.Errorf("puts = %v, want none once the object exists", store.puts)
		}
	})

	t.Run("stops waiting at the load deadline", func(t *testing.T) {
		store := &fakeBytecodeStore{}
		m := warmFixture(t, store, cacheDirWith(t, "compiled bytes"), "")

		start := time.Now()
		m.warmBytecodeCache(warmCtx(t, bytecodeUploadBudget+completionMargin+500*time.Millisecond))

		if elapsed := time.Since(start); elapsed > 2*time.Second {
			t.Errorf("took %s, want the wait ended at the load deadline", elapsed)
		}
	})

	t.Run("fails without asking when no window is left", func(t *testing.T) {
		store := &fakeBytecodeStore{}
		m := warmFixture(t, store, cacheDirWith(t, "compiled bytes"), warmedReply)

		got := m.warmBytecodeCache(warmCtx(t, bytecodeUploadBudget))

		if got.State != warmStateFailed {
			t.Fatalf("state = %q, want %q", got.State, warmStateFailed)
		}
		if len(store.heads) != 0 {
			t.Errorf("heads = %v, want the pass abandoned before any work", store.heads)
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
		want := deadline.Add(-bytecodeUploadBudget - completionMargin)
		if got.Sub(want) > time.Millisecond || want.Sub(got) > time.Millisecond {
			t.Errorf("load deadline = %s, want %s", got, want)
		}
	})

	t.Run("a deadline with nothing left to reserve yields no window", func(t *testing.T) {
		ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(bytecodeUploadBudget))
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
		if msg.Payload.CeilingBytes != bytecodeCacheCeiling {
			t.Errorf("ceilingBytes = %d, want %d", msg.Payload.CeilingBytes, bytecodeCacheCeiling)
		}
		want := deadline.Add(-warmReplyMargin).UnixMilli()
		if msg.Payload.DeadlineMs != want {
			t.Errorf("deadlineMs = %d, want %d (the load deadline less the reply margin)", msg.Payload.DeadlineMs, want)
		}

		fmt.Fprintln(nodeConn, warmedReply)
		<-done
	})

	t.Run("no ops without a control connection", func(t *testing.T) {
		m := &Membrane{}
		if _, _, ok := m.warmCompileCache(context.Background(), time.Now().Add(time.Second)); ok {
			t.Error("warmCompileCache() ok = true, want false with no child attached")
		}
	})
}

func TestDrainControlWarm(t *testing.T) {
	t.Run("drops an unawaited warm reply", func(t *testing.T) {
		m, _, nodeConn := controlConnPair(t)
		waiter := m.registerWaiter("req-1")

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

		store := &fakeBytecodeStore{}
		m := warmFixture(t, store, cacheDirWith(t, "compiled bytes"), warmedReply)
		m.nodePort = portOf(t, node)
		m.client = newLoopbackClient()

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
		if len(store.puts) != 1 {
			t.Errorf("puts = %d, want the cache published during the warm invocation", len(store.puts))
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
		m := &Membrane{nodePort: portOf(t, node), client: newLoopbackClient()}

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
		membraneSide, nodeSide := net.Pipe()
		nodeSide.Close()
		store := &fakeBytecodeStore{putErr: errors.New("access denied")}
		u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: cacheDirWith(t, "x"), OK: true}, true)
		m := &Membrane{control: membraneSide, pending: map[string]chan struct{}{}, bytecode: u}
		go m.drainControl(bufio.NewReader(membraneSide))

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
		for _, s := range []warmSummary{{State: warmStateAlreadyCached, Source: bytecodeSourceNone}, {State: warmStateDisabled, Source: bytecodeSourceNone}} {
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
