package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"slices"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"connectrpc.com/connect"
	connectvalidate "connectrpc.com/validate"

	"github.com/ocelhq/ocel/cli/internal/sourcearchive"
	cloudv1 "github.com/ocelhq/ocel/pkg/proto/cloud/v1"
	"github.com/ocelhq/ocel/pkg/proto/cloud/v1/cloudv1connect"
)

const token = "cloud-token"

type fakeCloud struct {
	cloudv1connect.UnimplementedUploadServiceHandler

	mu         sync.Mutex
	stored     map[string][]byte
	asked      [][]string
	committed  *cloudv1.SourceManifest
	bearers    []string
	refuse     map[string][]int
	retryAfter string
	inFlight   atomic.Int32
	peak       atomic.Int32
}

func serve(t *testing.T, fake *fakeCloud) *httptest.Server {
	t.Helper()
	if fake.stored == nil {
		fake.stored = map[string][]byte{}
	}
	mux := http.NewServeMux()
	mux.Handle(cloudv1connect.NewUploadServiceHandler(fake, connect.WithInterceptors(connectvalidate.NewInterceptor())))
	mux.HandleFunc("PUT /v1/blobs/{digest}", fake.put)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.bearer(r.Header)
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(server.Close)
	return server
}

func (f *fakeCloud) bearer(header http.Header) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.bearers = append(f.bearers, header.Get("Authorization"))
}

func (f *fakeCloud) Missing(_ context.Context, req *cloudv1.MissingRequest) (*cloudv1.MissingResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, req.GetDigests())
	var missing []string
	for _, digest := range req.GetDigests() {
		if _, ok := f.stored[digest]; !ok {
			missing = append(missing, digest)
		}
	}
	return &cloudv1.MissingResponse{Digests: missing}, nil
}

func (f *fakeCloud) CommitSource(_ context.Context, req *cloudv1.CommitSourceRequest) (*cloudv1.CommitSourceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, file := range req.GetManifest().GetFiles() {
		if _, ok := f.stored[file.GetDigest()]; file.GetDigest() != "" && !ok {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s is missing", file.GetDigest()))
		}
	}
	f.committed = req.GetManifest()
	return &cloudv1.CommitSourceResponse{SourceId: "src_1"}, nil
}

func (f *fakeCloud) put(w http.ResponseWriter, r *http.Request) {
	now := f.inFlight.Add(1)
	defer f.inFlight.Add(-1)
	for {
		peak := f.peak.Load()
		if now <= peak || f.peak.CompareAndSwap(peak, now) {
			break
		}
	}
	time.Sleep(5 * time.Millisecond)
	digest := r.PathValue("digest")
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if codes := f.refuse[digest]; len(codes) > 0 {
		f.refuse[digest] = codes[1:]
		if f.retryAfter != "" {
			w.Header().Set("Retry-After", f.retryAfter)
		}
		http.Error(w, "not now", codes[0])
		return
	}
	if sum(body) != digest {
		http.Error(w, "the body does not hash to the digest", http.StatusBadRequest)
		return
	}
	f.stored[digest] = body
	w.WriteHeader(http.StatusCreated)
}

type staticTokens struct{ calls atomic.Int32 }

func (s *staticTokens) Ensure(context.Context) (string, error) {
	s.calls.Add(1)
	return token, nil
}

type memorySource map[string]string

func (m memorySource) Blobs() []sourcearchive.Blob {
	var blobs []sourcearchive.Blob
	for _, body := range m {
		blobs = append(blobs, sourcearchive.Blob{Digest: sum(body), Size: uint64(len(body))})
	}
	slices.SortFunc(blobs, func(x, y sourcearchive.Blob) int { return strings.Compare(x.Digest, y.Digest) })
	return blobs
}

func (m memorySource) Open(digest string) (io.ReadCloser, error) {
	for _, body := range m {
		if sum(body) == digest {
			return io.NopCloser(strings.NewReader(body)), nil
		}
	}
	return nil, fmt.Errorf("no %s", digest)
}

func (m memorySource) manifest() *cloudv1.SourceManifest {
	manifest := &cloudv1.SourceManifest{}
	for name, body := range m {
		manifest.Files = append(manifest.Files, &cloudv1.SourceFile{Path: name, Digest: sum(body), Size: uint64(len(body))})
	}
	slices.SortFunc(manifest.Files, func(x, y *cloudv1.SourceFile) int { return strings.Compare(x.GetPath(), y.GetPath()) })
	return manifest
}

func sum[T string | []byte](body T) string {
	digest := sha256.Sum256([]byte(body))
	return hex.EncodeToString(digest[:])
}

func newTestClient(server *httptest.Server, tokens TokenSource) (*Client, *[]time.Duration) {
	c := New(server.URL+"/", tokens)
	var slept []time.Duration
	var mu sync.Mutex
	c.sleep = func(_ context.Context, d time.Duration) error {
		mu.Lock()
		defer mu.Unlock()
		slept = append(slept, d)
		return nil
	}
	return c, &slept
}

func upload(t *testing.T, c *Client, source memorySource) (Planned, string, []Progress, error) {
	t.Helper()
	planned, err := c.Plan(t.Context(), source)
	if err != nil {
		return planned, "", nil, err
	}
	var mu sync.Mutex
	var seen []Progress
	if err := c.Send(t.Context(), source, planned, func(p Progress) {
		mu.Lock()
		defer mu.Unlock()
		seen = append(seen, p)
	}); err != nil {
		return planned, "", seen, err
	}
	id, err := c.Commit(t.Context(), source.manifest())
	return planned, id, seen, err
}

func TestUploadSendsOnlyTheBlobsOcelCloudLacksThenCommitsTheManifest(t *testing.T) {
	source := memorySource{"a.ts": "alpha", "b.ts": "bravo", "c.ts": "charlie"}
	fake := &fakeCloud{stored: map[string][]byte{sum("alpha"): []byte("alpha")}}
	server := serve(t, fake)
	tokens := &staticTokens{}
	c, _ := newTestClient(server, tokens)

	planned, id, seen, err := upload(t, c, source)
	if err != nil {
		t.Fatal(err)
	}
	if id != "src_1" {
		t.Fatalf("source id = %q, want what CommitSource answered", id)
	}
	if len(planned.Missing) != 2 || planned.TotalBytes != uint64(len("bravo")+len("charlie")) {
		t.Fatalf("planned = %+v, want bravo and charlie", planned)
	}
	for _, body := range []string{"bravo", "charlie"} {
		if string(fake.stored[sum(body)]) != body {
			t.Fatalf("Ocel Cloud never received %q", body)
		}
	}
	if len(fake.committed.GetFiles()) != 3 {
		t.Fatalf("committed %v, want every file", fake.committed)
	}
	last := seen[len(seen)-1]
	if last.SentBytes != planned.TotalBytes || last.SentFiles != 2 || last.TotalFiles != 2 {
		t.Fatalf("last progress = %+v, want every byte and file sent", last)
	}
	for _, bearer := range fake.bearers {
		if bearer != "Bearer "+token {
			t.Fatalf("a request carried %q, want the Ocel Cloud token", bearer)
		}
	}
	if int(tokens.calls.Load()) != len(fake.bearers) {
		t.Fatalf("asked for a token %d times for %d requests, want once per request", tokens.calls.Load(), len(fake.bearers))
	}
}

func TestUploadAsksForMissingBlobsInBatches(t *testing.T) {
	source := memorySource{}
	for i := range MaxDigestsPerCall + 5 {
		source[fmt.Sprintf("f%d", i)] = fmt.Sprintf("body %d", i)
	}
	fake := &fakeCloud{stored: map[string][]byte{}}
	for _, blob := range source.Blobs() {
		fake.stored[blob.Digest] = nil
	}
	c, _ := newTestClient(serve(t, fake), &staticTokens{})
	planned, err := c.Plan(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if len(planned.Missing) != 0 {
		t.Fatalf("missing = %d, want none", len(planned.Missing))
	}
	if len(fake.asked) != 2 || len(fake.asked[0]) != MaxDigestsPerCall || len(fake.asked[1]) != 5 {
		t.Fatalf("asked in batches of %v, want %d then 5", batchSizes(fake.asked), MaxDigestsPerCall)
	}
}

func batchSizes(asked [][]string) []int {
	sizes := make([]int, 0, len(asked))
	for _, batch := range asked {
		sizes = append(sizes, len(batch))
	}
	return sizes
}

func TestUploadSendsAtMostEightBlobsAtOnce(t *testing.T) {
	source := memorySource{}
	for i := range 40 {
		source[fmt.Sprintf("f%d", i)] = fmt.Sprintf("body %d", i)
	}
	fake := &fakeCloud{}
	c, _ := newTestClient(serve(t, fake), &staticTokens{})
	if _, _, _, err := upload(t, c, source); err != nil {
		t.Fatal(err)
	}
	if peak := fake.peak.Load(); peak > putConcurrency || peak < 2 {
		t.Fatalf("at most %d uploads ran at once, want between 2 and %d", peak, putConcurrency)
	}
}

func TestUploadRetriesABlobOcelCloudCouldNotTakeYet(t *testing.T) {
	for name, tt := range map[string]struct {
		codes      []int
		retryAfter string
		wantSleep  time.Duration
	}{
		"a server error":                 {codes: []int{http.StatusBadGateway, http.StatusServiceUnavailable}},
		"too many requests, retry after": {codes: []int{http.StatusTooManyRequests}, retryAfter: "7", wantSleep: 7 * time.Second},
	} {
		t.Run(name, func(t *testing.T) {
			source := memorySource{"a.ts": "alpha"}
			fake := &fakeCloud{refuse: map[string][]int{sum("alpha"): tt.codes}, retryAfter: tt.retryAfter}
			c, slept := newTestClient(serve(t, fake), &staticTokens{})
			_, _, seen, err := upload(t, c, source)
			if err != nil {
				t.Fatal(err)
			}
			if len(*slept) != len(tt.codes) {
				t.Fatalf("slept %v, want once per refusal", *slept)
			}
			if tt.wantSleep != 0 && (*slept)[0] != tt.wantSleep {
				t.Fatalf("slept %v, want the %v Retry-After asked", *slept, tt.wantSleep)
			}
			if last := seen[len(seen)-1]; last.SentBytes != 5 {
				t.Fatalf("progress ended at %+v, want each byte counted once across retries", last)
			}
		})
	}
}

func TestUploadGivesUpOnABlobAfterItsAttempts(t *testing.T) {
	source := memorySource{"a.ts": "alpha"}
	codes := slices.Repeat([]int{http.StatusInternalServerError}, attempts)
	fake := &fakeCloud{refuse: map[string][]int{sum("alpha"): codes}}
	c, slept := newTestClient(serve(t, fake), &staticTokens{})
	_, _, _, err := upload(t, c, source)
	if err == nil || !strings.Contains(err.Error(), "gave up after 5 attempts") {
		t.Fatalf("err = %v, want it to give up", err)
	}
	if len(*slept) != attempts-1 {
		t.Fatalf("slept %d times, want between each attempt", len(*slept))
	}
}

func TestUploadDoesNotRetryABlobOcelCloudRefused(t *testing.T) {
	source := memorySource{"a.ts": "alpha"}
	fake := &fakeCloud{refuse: map[string][]int{sum("alpha"): {http.StatusForbidden}}}
	c, slept := newTestClient(serve(t, fake), &staticTokens{})
	_, _, _, err := upload(t, c, source)
	if err == nil || !strings.Contains(err.Error(), "403") {
		t.Fatalf("err = %v, want the refusal", err)
	}
	if len(*slept) != 0 {
		t.Fatalf("slept %v, want no retry", *slept)
	}
}

func TestUploadRetriesWhenOcelCloudIsUnreachable(t *testing.T) {
	var calls atomic.Int32
	fake := &fakeCloud{stored: map[string][]byte{}}
	inner := serve(t, fake)
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			hijacked, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				hijacked.Close()
			}
			return
		}
		proxied, err := http.NewRequestWithContext(r.Context(), r.Method, inner.URL+r.URL.Path, r.Body)
		if err != nil {
			t.Error(err)
			return
		}
		proxied.Header = r.Header
		proxied.ContentLength = r.ContentLength
		resp, err := http.DefaultClient.Do(proxied)
		if err != nil {
			t.Error(err)
			return
		}
		defer resp.Body.Close()
		for k, v := range resp.Header {
			w.Header()[k] = v
		}
		w.WriteHeader(resp.StatusCode)
		_, _ = io.Copy(w, resp.Body)
	}))
	t.Cleanup(flaky.Close)
	c, slept := newTestClient(flaky, &staticTokens{})
	planned := Planned{Missing: memorySource{"a.ts": "alpha"}.Blobs(), TotalBytes: 5}
	if err := c.Send(t.Context(), memorySource{"a.ts": "alpha"}, planned, nil); err != nil {
		t.Fatal(err)
	}
	if len(*slept) != 1 || fake.stored[sum("alpha")] == nil {
		t.Fatalf("slept %v, stored %v: want one retry that lands the blob", *slept, fake.stored)
	}
}

func TestCommitSurfacesWhatOcelCloudRefused(t *testing.T) {
	source := memorySource{"a.ts": "alpha"}
	c, _ := newTestClient(serve(t, &fakeCloud{}), &staticTokens{})
	_, err := c.Commit(t.Context(), source.manifest())
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("err = %v, want the failed precondition Ocel Cloud answered", err)
	}
}

func TestRetryAfterReadsSecondsAndDates(t *testing.T) {
	if got := retryAfter("12"); got != 12*time.Second {
		t.Fatalf("seconds: %v", got)
	}
	if got := retryAfter(time.Now().Add(time.Minute).UTC().Format(http.TimeFormat)); got < 50*time.Second || got > time.Minute {
		t.Fatalf("date: %v", got)
	}
	if got := retryAfter("soon"); got != 0 {
		t.Fatalf("garbage: %v", got)
	}
	if got := backoff(1, time.Hour); got != maxRetryAfter {
		t.Fatalf("a Retry-After past the ceiling waits %v, want %v", got, maxRetryAfter)
	}
	for attempt := 1; attempt <= attempts; attempt++ {
		ceiling := min(firstBackoff<<(attempt-1), maxBackoff)
		if got := backoff(attempt, 0); got < ceiling/2 || got >= ceiling {
			t.Fatalf("attempt %d waits %v, want within [%v, %v)", attempt, got, ceiling/2, ceiling)
		}
	}
}

func TestUploadNeverSendsTheTokenToAnotherHostABlobIsRedirectedTo(t *testing.T) {
	elsewhere := &fakeCloud{}
	moved, err := url.Parse(serve(t, elsewhere).URL)
	if err != nil {
		t.Fatal(err)
	}
	moved.Host = "localhost:" + moved.Port()
	redirecting := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, moved.String()+r.URL.Path, http.StatusFound)
	}))
	t.Cleanup(redirecting.Close)
	source := memorySource{"a.ts": "alpha"}
	c, _ := newTestClient(redirecting, &staticTokens{})

	_ = c.Send(t.Context(), source, Planned{Missing: source.Blobs()}, nil)
	elsewhere.mu.Lock()
	defer elsewhere.mu.Unlock()
	if len(elsewhere.bearers) == 0 {
		t.Fatal("the redirect was never followed")
	}
	for _, bearer := range elsewhere.bearers {
		if bearer != "" {
			t.Fatalf("the other host was sent %q, want no token", bearer)
		}
	}
}
