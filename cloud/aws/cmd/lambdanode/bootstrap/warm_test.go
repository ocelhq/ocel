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

// warmedReply is what a Next artifact that knows the warm request answers
// with once it has walked its bundle.
const warmedReply = `{"type":"compile-cache-warmed","payload":` +
	`{"ok":true,"state":"warmed","entries":42,"loaded":41,` +
	`"failures":[{"entry":"app/broken/page.js","message":"boom"}],` +
	`"stoppedBy":"complete","bytes":1234,"dir":"/tmp/.ocel/compile-cache"}}`

// unsupportedReply is what an artifact built before the warm capability
// existed — or any non-Next one — answers with.
const unsupportedReply = `{"type":"compile-cache-warmed","payload":{"ok":false,"state":"unsupported","dir":null}}`

// stoppedReply is a walk the ceiling cut short, naming a bounded sample of what
// it never reached.
const stoppedReply = `{"type":"compile-cache-warmed","payload":` +
	`{"ok":true,"state":"warmed","entries":42,"loaded":33,"failures":[],` +
	`"stoppedBy":"ceiling","skipped":["app/a/page","app/b/page"],"skippedCount":9,` +
	`"bytes":1234,"dir":"/tmp/.ocel/compile-cache"}}`

// The shape is the whole authorization story: these Function URLs are AWS_IAM
// and the edge composes the event envelope itself, so public traffic can only
// ever influence headers and body. Every case below that carries the warm
// object inside a real event is exactly that attempt, and must not be taken
// for a warm invocation.
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

// warmRuntime serves one invocation carrying a real deadline and captures the
// raw payload the membrane answered with. Both are load-bearing: the load
// window is derived from that deadline, and a warm answer is the payload
// itself rather than a prelude-framed response.
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

// warmFixture wires a membrane to a stand-in node that answers the warm
// request with reply, and to a store under the test's control — the shape
// bringUpWithBytecode leaves behind on a rehydrate miss. An empty reply stands
// for a child that reads the request and never answers.
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

// slowBytecodeStore gives the publish leg a real duration, which is the window
// a report node was too slow to make the load deadline has to arrive in.
type slowBytecodeStore struct {
	*fakeBytecodeStore
	delay time.Duration
}

func (s slowBytecodeStore) putObject(ctx context.Context, bucket, key string, body []byte) error {
	time.Sleep(s.delay)
	return s.fakeBytecodeStore.putObject(ctx, bucket, key, body)
}

// warmCtx stands in for an invocation the platform gave the given time to run.
func warmCtx(t *testing.T, remaining time.Duration) context.Context {
	t.Helper()
	ctx, cancel := context.WithDeadline(context.Background(), time.Now().Add(remaining))
	t.Cleanup(cancel)
	return ctx
}

// The whole point of the inline publish: the deploy has to be able to tell
// "the cache landed" from "it did not", which a post-response upload only ever
// says in CloudWatch.
func TestWarmBytecodeCache_PublishesInlineAndReportsIt(t *testing.T) {
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
}

// The warm pass spends this instance's one upload attempt, so the ordinary
// post-invocation path must find nothing left to do rather than re-HEAD and
// re-archive the same object on the next request's billed time.
func TestWarmBytecodeCache_SpendsTheInstancesOneUpload(t *testing.T) {
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
}

// A rehydrate hit proves the object exists, so loading every entry could not
// publish anything — it would only burn the invocation. Answering immediately
// is what makes a deploy retry idempotent and near-free.
func TestWarmBytecodeCache_AlreadyCachedAnswersWithoutTouchingTheChild(t *testing.T) {
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
	// The key is the deploy's only way to reach the object it just had
	// published — it never learns node's version, so it cannot compose one.
	if got.Key != m.bytecodeKey {
		t.Errorf("key = %q, want the resolution's %q", got.Key, m.bytecodeKey)
	}
	if got.Source != bytecodeSourceS3 {
		t.Errorf("source = %q, want %q", got.Source, bytecodeSourceS3)
	}
}

// An embedded hit and an S3 hit are the same already-cached answer, and the
// deploy's whole verification of the embed pass is telling them apart.
func TestWarmBytecodeCache_AlreadyCachedNamesWhichLegServedIt(t *testing.T) {
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
}

// A pass that had to publish for itself read no cache at all, and says so
// rather than leaving the field empty for the deploy to interpret.
func TestWarmBytecodeCache_APassThatPublishesReportsNoSource(t *testing.T) {
	m := warmFixture(t, &fakeBytecodeStore{}, cacheDirWith(t, "compiled bytes"), warmedReply)

	got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

	if got.State != warmStatePublished {
		t.Fatalf("state = %q, want %q", got.State, warmStatePublished)
	}
	if got.Source != bytecodeSourceNone {
		t.Errorf("source = %q, want %q", got.Source, bytecodeSourceNone)
	}
}

// A deployment that resolved no bytecode identity at all has nothing to warm,
// and must say so rather than read as a failure the deploy should retry.
func TestWarmBytecodeCache_DisabledForAnUnconfiguredDeployment(t *testing.T) {
	m := &Membrane{}

	got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

	if got.State != warmStateDisabled {
		t.Errorf("state = %q, want %q", got.State, warmStateDisabled)
	}
}

// An artifact with no warm capability — non-Next, or a launcher built before
// warming existed — still loaded its primary entry at INIT, so there is a real
// compile cache on disk. Publishing nothing when something is available is the
// one outcome this feature exists to avoid.
func TestWarmBytecodeCache_UnsupportedArtifactStillPublishesWhatInitLoaded(t *testing.T) {
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
}

// A single overrunning require pushes node's report past the deadline it only
// checks between entries. The membrane does not need that report to publish —
// the cache directory is shared disk and the flush is its own call — so a walk
// that loaded the whole bundle must not report failure having published
// nothing, which is strictly worse than never warming at all.
func TestWarmBytecodeCache_PublishesWhenNodeNeverReportsBack(t *testing.T) {
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
}

// A report that lands while the publish leg runs is the late half of the same
// story, and its counts are the only account of what stayed cold. Dropping it
// with the waiter would throw them away for nothing.
func TestWarmBytecodeCache_CollectsALateReport(t *testing.T) {
	store := slowBytecodeStore{fakeBytecodeStore: &fakeBytecodeStore{}, delay: 400 * time.Millisecond}
	m, nodeReader, nodeConn := controlConnPair(t)
	u, _ := uploadFixture(store, compileCacheFlushedPayload{Dir: cacheDirWith(t, "compiled bytes"), OK: true}, true)
	m.bytecode = u

	// The reply arrives after the load deadline has passed but while the
	// publish is still in flight.
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
}

// The one thing an operator cannot work out from "38/51" is which routes stay
// cold, so the names travel from node's report all the way into the summary.
func TestWarmBytecodeCache_CarriesTheSkippedEntries(t *testing.T) {
	store := &fakeBytecodeStore{}
	m := warmFixture(t, store, cacheDirWith(t, "compiled bytes"), stoppedReply)

	got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

	if got.StoppedBy != "ceiling" || got.SkippedCount != 9 {
		t.Fatalf("summary = %+v, want the walk's own stop and skipped count", got)
	}
	if len(got.Skipped) != 2 || got.Skipped[0] != "app/a/page" {
		t.Errorf("skipped = %v, want the names node reported", got.Skipped)
	}
}

// A PUT that S3 refused is not a published cache, and the deploy has to be
// able to tell the two apart from the answer alone.
func TestWarmBytecodeCache_ReportsAFailedUploadAsFailed(t *testing.T) {
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
}

// A cache the warm pass grew past the ceiling is refused by the upload's own
// guard, and reads as failed with the ceiling named — not as published.
func TestWarmBytecodeCache_ReportsACacheOverTheCeiling(t *testing.T) {
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
}

// Another instance winning the race is the cache landing, not this pass
// failing: the PUT is create-if-absent and the object is there either way.
func TestWarmBytecodeCache_AnObjectAlreadyThereReadsAsAlreadyCached(t *testing.T) {
	store := &fakeBytecodeStore{exists: true}
	m := warmFixture(t, store, cacheDirWith(t, "compiled bytes"), warmedReply)

	got := m.warmBytecodeCache(warmCtx(t, 10*time.Second))

	if got.State != warmStateAlreadyCached {
		t.Fatalf("state = %q, want %q", got.State, warmStateAlreadyCached)
	}
	if len(store.puts) != 0 {
		t.Errorf("puts = %v, want none once the object exists", store.puts)
	}
}

// A child that reads the request and never answers must not hold the pass to
// the function timeout: the wait ends at the load deadline and the state says
// so.
// A child that reads the request and never answers must not hold the pass to
// the function timeout: the wait ends at the load deadline, and the publish leg
// still gets the budget that was reserved for it.
func TestWarmBytecodeCache_StopsWaitingAtTheLoadDeadline(t *testing.T) {
	store := &fakeBytecodeStore{}
	m := warmFixture(t, store, cacheDirWith(t, "compiled bytes"), "")

	start := time.Now()
	m.warmBytecodeCache(warmCtx(t, bytecodeUploadBudget+completionMargin+500*time.Millisecond))

	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("took %s, want the wait ended at the load deadline", elapsed)
	}
}

// The arithmetic is the whole reason the pass can publish anything: a load
// that ran to the invocation deadline would be killed mid-flush and publish
// nothing at all.
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

// A pass with no window left must not ask node to load anything: whatever it
// loaded could never be published, and the load is what would overrun.
func TestWarmBytecodeCache_FailsWithoutAskingWhenNoWindowIsLeft(t *testing.T) {
	store := &fakeBytecodeStore{}
	m := warmFixture(t, store, cacheDirWith(t, "compiled bytes"), warmedReply)

	got := m.warmBytecodeCache(warmCtx(t, bytecodeUploadBudget))

	if got.State != warmStateFailed {
		t.Fatalf("state = %q, want %q", got.State, warmStateFailed)
	}
	if len(store.heads) != 0 {
		t.Errorf("heads = %v, want the pass abandoned before any work", store.heads)
	}
}

// The ceiling travels in the request rather than being duplicated on the node
// side, and the deadline node is told is the membrane's own less the reply
// margin — so node stops loading early enough for its answer to arrive.
func TestWarmCompileCache_RequestCarriesTheDeadlineAndTheCeiling(t *testing.T) {
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
}

func TestWarmCompileCache_NoOpsWithoutAControlConnection(t *testing.T) {
	m := &Membrane{}
	if _, _, ok := m.warmCompileCache(context.Background(), time.Now().Add(time.Second)); ok {
		t.Error("warmCompileCache() ok = true, want false with no child attached")
	}
}

// A reply nobody is waiting for must not wedge the drain loop, which also
// carries invocation-complete.
func TestDrainControl_DropsAnUnawaitedWarmReply(t *testing.T) {
	m, _, nodeConn := controlConnPair(t)
	waiter := m.registerWaiter("req-1")

	fmt.Fprintln(nodeConn, warmedReply)
	fmt.Fprintln(nodeConn, `{"type":"invocation-complete","payload":{"requestId":"req-1"}}`)

	select {
	case <-waiter:
	case <-time.After(2 * time.Second):
		t.Fatal("drain loop stalled on a warm reply with no waiter")
	}
}

// The answer is the payload itself: no Function URL is involved, so the
// prelude framing forward() uses would be read as part of the deploy's JSON.
func TestHandleInvocation_WarmPayloadIsAnsweredWithoutForwarding(t *testing.T) {
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
}

// The other half of the same property: a real event still reaches node and
// still comes back prelude-framed.
func TestHandleInvocation_RealEventIsStillForwarded(t *testing.T) {
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
}

// A warm invocation must not fail the invocation or leave the runtime loop,
// however badly it goes — here the child is gone entirely and S3 refuses the
// PUT, so there is genuinely nothing to publish.
func TestHandleInvocation_AWarmFailureStillAnswersTheInvocation(t *testing.T) {
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
}

// Fields that do not apply are omitted rather than reported as zero: a deploy
// reading "loaded":0 on a cache that was already there would be reading a
// number this pass never measured.
func TestWarmSummary_OmitsWhatDoesNotApply(t *testing.T) {
	for _, s := range []warmSummary{{State: warmStateAlreadyCached, Source: bytecodeSourceNone}, {State: warmStateDisabled, Source: bytecodeSourceNone}} {
		encoded, err := json.Marshal(s)
		if err != nil {
			t.Fatal(err)
		}
		// source is the one field that is never omitted: "none" is a real
		// answer, and an absent field would read as an older membrane.
		want := `{"state":"` + s.State + `","source":"none"}`
		if string(encoded) != want {
			t.Errorf("summary = %s, want %s", encoded, want)
		}
	}
}
