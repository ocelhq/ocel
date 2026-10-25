package cloud

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
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

	method     string
	uploadBase string
	storageURL string

	mu             sync.Mutex
	stored         map[string][]byte
	asked          [][]string
	committed      *cloudv1.SourceManifest
	bearers        []string
	storageBearers []string
	posted         []postedForm
	refuse         map[string][]int
	retryAfter     string
	refused        error
	inFlight       atomic.Int32
	peak           atomic.Int32
}

type postedForm struct {
	fields        []string
	fileLast      bool
	contentLength int64
}

func serve(t *testing.T, fake *fakeCloud) *httptest.Server {
	t.Helper()
	if fake.stored == nil {
		fake.stored = map[string][]byte{}
	}
	storageMux := http.NewServeMux()
	storageMux.HandleFunc("PUT /blobs/{digest}", fake.put)
	storageMux.HandleFunc("POST /blobs", fake.post)
	storage := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fake.mu.Lock()
		fake.storageBearers = append(fake.storageBearers, r.Header.Get("Authorization"))
		fake.mu.Unlock()
		now := fake.inFlight.Add(1)
		defer fake.inFlight.Add(-1)
		for {
			peak := fake.peak.Load()
			if now <= peak || fake.peak.CompareAndSwap(peak, now) {
				break
			}
		}
		time.Sleep(5 * time.Millisecond)
		storageMux.ServeHTTP(w, r)
	}))
	t.Cleanup(storage.Close)
	fake.storageURL = storage.URL
	fake.uploadBase = storage.URL

	mux := http.NewServeMux()
	mux.Handle(cloudv1connect.NewUploadServiceHandler(fake, connect.WithInterceptors(connectvalidate.NewInterceptor())))
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

func signatureOf(digest string) string {
	return sum("signed " + digest)
}

func (f *fakeCloud) target(digest string) *cloudv1.Upload {
	if f.method == http.MethodPost {
		return &cloudv1.Upload{
			Digest: digest,
			Url:    f.uploadBase + "/blobs",
			Method: http.MethodPost,
			Fields: map[string]string{"key": digest, "policy": "e30=", "signature": signatureOf(digest)},
		}
	}
	return &cloudv1.Upload{
		Digest:  digest,
		Url:     f.uploadBase + "/blobs/" + digest + "?signature=" + signatureOf(digest),
		Method:  http.MethodPut,
		Headers: map[string]string{"Upload-Checksum-Sha256": digest},
	}
}

func (f *fakeCloud) Missing(_ context.Context, req *cloudv1.MissingRequest) (*cloudv1.MissingResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.asked = append(f.asked, req.GetDigests())
	if f.refused != nil {
		return nil, f.refused
	}
	missing := &cloudv1.MissingResponse{}
	for _, digest := range req.GetDigests() {
		if _, ok := f.stored[digest]; !ok {
			missing.Uploads = append(missing.Uploads, f.target(digest))
		}
	}
	return missing, nil
}

func (f *fakeCloud) CommitSource(_ context.Context, req *cloudv1.CommitSourceRequest) (*cloudv1.CommitSourceResponse, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.refused != nil {
		return nil, f.refused
	}
	for _, file := range req.GetManifest().GetFiles() {
		if _, ok := f.stored[file.GetDigest()]; file.GetDigest() != "" && !ok {
			return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s is missing", file.GetDigest()))
		}
	}
	f.committed = req.GetManifest()
	return &cloudv1.CommitSourceResponse{SourceId: "src_1"}, nil
}

func (f *fakeCloud) put(w http.ResponseWriter, r *http.Request) {
	digest := r.PathValue("digest")
	if r.URL.Query().Get("signature") != signatureOf(digest) {
		http.Error(w, "the signature does not match", http.StatusForbidden)
		return
	}
	if r.Header.Get("Upload-Checksum-Sha256") != digest {
		http.Error(w, "the upload names no checksum", http.StatusBadRequest)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	f.store(w, digest, body)
}

func (f *fakeCloud) post(w http.ResponseWriter, r *http.Request) {
	form, err := r.MultipartReader()
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	fields := map[string]string{}
	posted := postedForm{contentLength: r.ContentLength}
	var body []byte
	for {
		part, err := form.NextPart()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		read, err := io.ReadAll(part)
		if err != nil {
			http.Error(w, err.Error(), http.StatusBadRequest)
			return
		}
		if part.FormName() == "file" {
			body = read
			_, err := form.NextPart()
			posted.fileLast = errors.Is(err, io.EOF)
			break
		}
		fields[part.FormName()] = string(read)
		posted.fields = append(posted.fields, part.FormName())
	}
	f.mu.Lock()
	f.posted = append(f.posted, posted)
	f.mu.Unlock()
	digest := fields["key"]
	if fields["signature"] != signatureOf(digest) {
		http.Error(w, "the policy signature does not match", http.StatusForbidden)
		return
	}
	f.store(w, digest, body)
}

func (f *fakeCloud) store(w http.ResponseWriter, digest string, body []byte) {
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

func TestUploadSendsEachBlobAsTheBodyOfThePutItsTargetNames(t *testing.T) {
	source := memorySource{"a.ts": "alpha", "b.ts": "bravo"}
	fake := &fakeCloud{method: http.MethodPut}
	c, _ := newTestClient(serve(t, fake), &staticTokens{})

	if _, _, _, err := upload(t, c, source); err != nil {
		t.Fatalf("upload through PUT targets = %v, want both blobs stored under the target's signature and headers", err)
	}
	for _, body := range []string{"alpha", "bravo"} {
		if string(fake.stored[sum(body)]) != body {
			t.Fatalf("the PUT target never received %q", body)
		}
	}
}

func TestUploadSendsEachBlobAsTheFileFieldOfThePostPolicyItsTargetNames(t *testing.T) {
	source := memorySource{"a.ts": "alpha", "b.ts": "bravo"}
	fake := &fakeCloud{method: http.MethodPost}
	c, _ := newTestClient(serve(t, fake), &staticTokens{})

	if _, _, seen, err := upload(t, c, source); err != nil {
		t.Fatalf("upload through POST policy targets = %v, want both blobs stored", err)
	} else if last := seen[len(seen)-1]; last.SentBytes != uint64(len("alpha")+len("bravo")) {
		t.Fatalf("progress ended at %+v, want only the files' bytes counted", last)
	}
	for _, body := range []string{"alpha", "bravo"} {
		if string(fake.stored[sum(body)]) != body {
			t.Fatalf("the POST policy target never received %q", body)
		}
	}
	if len(fake.posted) != 2 {
		t.Fatalf("posted %d forms, want one per blob", len(fake.posted))
	}
	for _, form := range fake.posted {
		slices.Sort(form.fields)
		if !slices.Equal(form.fields, []string{"key", "policy", "signature"}) || !form.fileLast {
			t.Fatalf("posted fields %v then a file last = %v, want every policy field before the file, and the file last", form.fields, form.fileLast)
		}
		if form.contentLength <= 0 {
			t.Fatalf("a form was posted with content length %d, want its length known, as a POST policy requires", form.contentLength)
		}
	}
}

func TestUploadNeverSendsTheOcelCloudTokenToAnUploadTarget(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			fake := &fakeCloud{method: method}
			c, _ := newTestClient(serve(t, fake), &staticTokens{})
			if _, _, _, err := upload(t, c, memorySource{"a.ts": "alpha"}); err != nil {
				t.Fatal(err)
			}
			if len(fake.storageBearers) == 0 {
				t.Fatal("the upload target was never reached")
			}
			for _, bearer := range fake.storageBearers {
				if bearer != "" {
					t.Fatalf("the upload target was sent %q, want no token: the target's own signature is its credential", bearer)
				}
			}
		})
	}
}

func TestCommitIsRefusedWhileABlobItsManifestNamesWasNeverUploaded(t *testing.T) {
	source := memorySource{"a.ts": "alpha"}
	c, _ := newTestClient(serve(t, &fakeCloud{}), &staticTokens{})
	planned, err := c.Plan(t.Context(), source)
	if err != nil || len(planned.Missing) != 1 {
		t.Fatalf("Plan() = %+v, %v, want alpha missing", planned, err)
	}

	_, err = c.Commit(t.Context(), source.manifest())
	if connect.CodeOf(err) != connect.CodeFailedPrecondition {
		t.Fatalf("err = %v, want the failed precondition Ocel Cloud answered", err)
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
	if peak := fake.peak.Load(); peak > uploadConcurrency || peak < 2 {
		t.Fatalf("at most %d uploads ran at once, want between 2 and %d", peak, uploadConcurrency)
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

func TestUploadDoesNotRetryABlobItsTargetRefusedAndSaysWhatTheTargetAnswered(t *testing.T) {
	for _, method := range []string{http.MethodPut, http.MethodPost} {
		t.Run(method, func(t *testing.T) {
			source := memorySource{"a.ts": "alpha"}
			fake := &fakeCloud{method: method, refuse: map[string][]int{sum("alpha"): {http.StatusForbidden}}}
			c, slept := newTestClient(serve(t, fake), &staticTokens{})
			_, _, _, err := upload(t, c, source)
			if err == nil || !strings.Contains(err.Error(), "403") || !strings.Contains(err.Error(), "not now") {
				t.Fatalf("err = %v, want the status and what the target said", err)
			}
			if strings.Contains(err.Error(), signatureOf(sum("alpha"))) {
				t.Fatalf("err = %v names the target's signature, a credential", err)
			}
			if len(*slept) != 0 {
				t.Fatalf("slept %v, want no retry", *slept)
			}
		})
	}
}

func TestUploadNamesNoSignatureOfATargetItCouldNotReach(t *testing.T) {
	gone := httptest.NewServer(http.NotFoundHandler())
	gone.Close()
	source := memorySource{"a.ts": "alpha"}
	fake := &fakeCloud{}
	c, _ := newTestClient(serve(t, fake), &staticTokens{})
	fake.uploadBase = gone.URL
	planned, err := c.Plan(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}

	err = c.Send(t.Context(), source, planned, nil)
	if err == nil {
		t.Fatal("Send() to a target nothing listens on = nil")
	}
	if strings.Contains(err.Error(), signatureOf(sum("alpha"))) {
		t.Fatalf("err = %v names the target's signature, a credential", err)
	}
	if host := strings.TrimPrefix(gone.URL, "http://"); !strings.Contains(err.Error(), host) {
		t.Fatalf("err = %v, want the host it could not reach, %s", err, host)
	}
}

func TestUploadRetriesWhenItsTargetIsUnreachable(t *testing.T) {
	var calls atomic.Int32
	fake := &fakeCloud{}
	api := serve(t, fake)
	storage := fake.storageURL
	flaky := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if calls.Add(1) == 1 {
			hijacked, _, err := w.(http.Hijacker).Hijack()
			if err == nil {
				hijacked.Close()
			}
			return
		}
		proxied, err := http.NewRequestWithContext(r.Context(), r.Method, storage+r.URL.RequestURI(), r.Body)
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
	fake.uploadBase = flaky.URL
	c, slept := newTestClient(api, &staticTokens{})
	source := memorySource{"a.ts": "alpha"}
	planned, err := c.Plan(t.Context(), source)
	if err != nil {
		t.Fatal(err)
	}
	if err := c.Send(t.Context(), source, planned, nil); err != nil {
		t.Fatal(err)
	}
	if len(*slept) != 1 || fake.stored[sum("alpha")] == nil {
		t.Fatalf("slept %v, stored %v: want one retry that lands the blob", *slept, fake.stored)
	}
}

func clientWhoseMissingAnswers(t *testing.T, tamper func(fake *fakeCloud, missing *cloudv1.MissingResponse)) *Client {
	t.Helper()
	fake := &fakeCloud{}
	api := serve(t, fake)
	c, _ := newTestClient(api, &staticTokens{})
	c.upload = cloudv1connect.NewUploadServiceClient(http.DefaultClient, api.URL, connect.WithInterceptors(connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			res, err := next(ctx, req)
			if missing, ok := res.Any().(*cloudv1.MissingResponse); ok {
				tamper(fake, missing)
			}
			return res, err
		}
	})))
	return c
}

func TestPlanRefusesATargetForABlobItNeverAskedAbout(t *testing.T) {
	stranger := sum("never asked")
	c := clientWhoseMissingAnswers(t, func(fake *fakeCloud, missing *cloudv1.MissingResponse) {
		missing.Uploads = append(missing.Uploads, fake.target(stranger))
	})

	_, err := c.Plan(t.Context(), memorySource{"a.ts": "alpha"})
	if err == nil || !strings.Contains(err.Error(), stranger) {
		t.Fatalf("Plan() = %v, want the target for %s refused", err, stranger)
	}
}

func TestPlanRefusesAnAnswerThatBreaksTheUploadTargetRules(t *testing.T) {
	for name, tamper := range map[string]func(fake *fakeCloud, missing *cloudv1.MissingResponse){
		"a digest named twice": func(fake *fakeCloud, missing *cloudv1.MissingResponse) {
			missing.Uploads = append(missing.Uploads, fake.target(sum("alpha")))
		},
		"a method other than PUT or POST": func(_ *fakeCloud, missing *cloudv1.MissingResponse) {
			missing.Uploads[0].Method = http.MethodPatch
		},
		"a url that is not http": func(_ *fakeCloud, missing *cloudv1.MissingResponse) {
			missing.Uploads[0].Url = "file:///etc/passwd"
		},
		"form fields on a PUT": func(_ *fakeCloud, missing *cloudv1.MissingResponse) {
			missing.Uploads[0].Fields = map[string]string{"key": "x"}
		},
	} {
		t.Run(name, func(t *testing.T) {
			c := clientWhoseMissingAnswers(t, tamper)

			if planned, err := c.Plan(t.Context(), memorySource{"a.ts": "alpha"}); err == nil {
				t.Fatalf("Plan() planned %d uploads, want the answer refused", len(planned.Missing))
			}
		})
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
