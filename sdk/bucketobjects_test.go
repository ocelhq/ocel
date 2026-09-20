package ocel_test

import (
	"bytes"
	"errors"
	"io"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/channel"
	"github.com/ocelhq/ocel/pkg/constants"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	ocel "github.com/ocelhq/ocel/sdk"
)

func TestABucketReadsBackWhatItWrote(t *testing.T) {
	store := bucketFixture(t, newFakeStore())

	err := store.WriteAll(t.Context(), "a/b.txt", []byte("hello"),
		ocel.ContentType("text/plain"),
		ocel.CacheControl("max-age=60"),
		ocel.Metadata(map[string]string{"owner": "ada"}),
	)
	if err != nil {
		t.Fatalf("WriteAll() error = %v", err)
	}

	got, err := store.ReadAll(t.Context(), "a/b.txt")
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(got) != "hello" {
		t.Errorf("ReadAll() = %q, want %q", got, "hello")
	}

	attrs, err := store.Attrs(t.Context(), "a/b.txt")
	if err != nil {
		t.Fatalf("Attrs() error = %v", err)
	}
	if attrs.Key != "a/b.txt" || attrs.Size != 5 || attrs.ContentType != "text/plain" {
		t.Errorf("Attrs() = %+v", attrs)
	}
	if attrs.Metadata["owner"] != "ada" {
		t.Errorf("Attrs().Metadata = %v, want the metadata the write carried", attrs.Metadata)
	}
	if attrs.ETag == "" || attrs.UploadedAt.IsZero() {
		t.Errorf("Attrs() = %+v, want an etag and an upload time", attrs)
	}
}

func TestABodyOneRequestCarriesStartsNoMultipartUpload(t *testing.T) {
	fake := newFakeStore()
	store := bucketFixture(t, fake)

	if err := store.WriteAll(t.Context(), "small", []byte("a few bytes")); err != nil {
		t.Fatalf("WriteAll() error = %v", err)
	}
	if len(fake.created) != 0 {
		t.Errorf("multipart uploads = %v, want a small body to go up in one request", fake.created)
	}
}

func TestAWriterStreamsIntoOneObject(t *testing.T) {
	store := bucketFixture(t, newFakeStore())

	w := store.NewWriter(t.Context(), "notes.txt")
	for _, line := range []string{"one\n", "two\n", "three\n"} {
		if _, err := io.WriteString(w, line); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	got, err := store.ReadAll(t.Context(), "notes.txt")
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(got) != "one\ntwo\nthree\n" {
		t.Errorf("ReadAll() = %q", got)
	}
}

func TestARangeReaderReadsTheBytesItAsksFor(t *testing.T) {
	store := bucketFixture(t, newFakeStore())
	if err := store.WriteAll(t.Context(), "alphabet", []byte("abcdefghij")); err != nil {
		t.Fatalf("WriteAll() error = %v", err)
	}

	reader, err := store.NewRangeReader(t.Context(), "alphabet", 2, 3)
	if err != nil {
		t.Fatalf("NewRangeReader() error = %v", err)
	}
	defer reader.Close()

	got, err := io.ReadAll(reader)
	if err != nil {
		t.Fatalf("read the range: %v", err)
	}
	if string(got) != "cde" {
		t.Errorf("range = %q, want %q", got, "cde")
	}
	if attrs := reader.Attrs(); attrs.Size != 10 {
		t.Errorf("Attrs().Size = %d, want the whole object's length", attrs.Size)
	}

	tail, err := store.NewRangeReader(t.Context(), "alphabet", 7, -1)
	if err != nil {
		t.Fatalf("NewRangeReader() to the end error = %v", err)
	}
	defer tail.Close()
	rest, err := io.ReadAll(tail)
	if err != nil {
		t.Fatalf("read the tail: %v", err)
	}
	if string(rest) != "hij" {
		t.Errorf("tail = %q, want %q", rest, "hij")
	}
}

func TestAKeyTheBucketDoesNotHoldIsAnErrObjectNotFound(t *testing.T) {
	store := bucketFixture(t, newFakeStore())

	for _, tc := range []struct {
		access string
		err    error
	}{
		{"Attrs", second(store.Attrs(t.Context(), "missing"))},
		{"NewReader", second(store.NewReader(t.Context(), "missing"))},
		{"ReadAll", second(store.ReadAll(t.Context(), "missing"))},
		{"NewRangeReader", second(store.NewRangeReader(t.Context(), "missing", 0, 4))},
		{"Copy", second(store.Copy(t.Context(), "there", "missing"))},
	} {
		if !errors.Is(tc.err, ocel.ErrObjectNotFound) {
			t.Errorf("%s() error = %v, want ocel.ErrObjectNotFound", tc.access, tc.err)
		}
		if tc.err != nil && !strings.Contains(tc.err.Error(), "missing") {
			t.Errorf("%s() error = %q, want it to name the key", tc.access, tc.err)
		}
	}

	held, err := store.Exists(t.Context(), "missing")
	if err != nil {
		t.Fatalf("Exists() error = %v", err)
	}
	if held {
		t.Error("Exists() = true, want false for a key the bucket does not hold")
	}
}

func TestAConditionalWriteRefusesWithErrPreconditionFailed(t *testing.T) {
	store := bucketFixture(t, newFakeStore())
	if err := store.WriteAll(t.Context(), "once", []byte("first")); err != nil {
		t.Fatalf("WriteAll() error = %v", err)
	}

	err := store.WriteAll(t.Context(), "once", []byte("second"), ocel.IfNotExists())
	if !errors.Is(err, ocel.ErrPreconditionFailed) {
		t.Errorf("WriteAll(IfNotExists) error = %v, want ocel.ErrPreconditionFailed", err)
	}

	err = store.WriteAll(t.Context(), "once", []byte("second"), ocel.IfMatch("etag-nothing"))
	if !errors.Is(err, ocel.ErrPreconditionFailed) {
		t.Errorf("WriteAll(IfMatch) error = %v, want ocel.ErrPreconditionFailed", err)
	}

	attrs, err := store.Attrs(t.Context(), "once")
	if err != nil {
		t.Fatalf("Attrs() error = %v", err)
	}
	if err := store.WriteAll(t.Context(), "once", []byte("third"), ocel.IfMatch(attrs.ETag)); err != nil {
		t.Fatalf("WriteAll(IfMatch) on the current etag error = %v", err)
	}
	got, err := store.ReadAll(t.Context(), "once")
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if string(got) != "third" {
		t.Errorf("ReadAll() = %q, want the conditional write to have landed", got)
	}
}

func TestAListingWalksEveryPageItself(t *testing.T) {
	store := bucketFixture(t, newFakeStore())
	for _, key := range []string{"a/1", "a/2", "a/3", "a/4", "a/5", "b/1"} {
		if err := store.WriteAll(t.Context(), key, []byte(key)); err != nil {
			t.Fatalf("WriteAll(%q) error = %v", key, err)
		}
	}

	var walked []string
	for held, err := range store.List(t.Context(), ocel.Prefix("a/"), ocel.Limit(2)) {
		if err != nil {
			t.Fatalf("List() error = %v", err)
		}
		walked = append(walked, held.Key)
	}

	want := []string{"a/1", "a/2", "a/3", "a/4", "a/5"}
	if strings.Join(walked, ",") != strings.Join(want, ",") {
		t.Errorf("List() walked %v, want %v", walked, want)
	}
}

func TestDeletingAKeyTheBucketDoesNotHoldIsSilent(t *testing.T) {
	store := bucketFixture(t, newFakeStore())
	if err := store.WriteAll(t.Context(), "here", []byte("x")); err != nil {
		t.Fatalf("WriteAll() error = %v", err)
	}

	if err := store.Delete(t.Context(), "here", "never-there"); err != nil {
		t.Fatalf("Delete() error = %v", err)
	}
	if err := store.Delete(t.Context()); err != nil {
		t.Fatalf("Delete() with no keys error = %v", err)
	}

	held, err := store.Exists(t.Context(), "here")
	if err != nil {
		t.Fatalf("Exists() error = %v", err)
	}
	if held {
		t.Error("Exists() = true after the key was deleted")
	}
}

func TestCopyLeavesTheSourceWhereItWas(t *testing.T) {
	store := bucketFixture(t, newFakeStore())
	if err := store.WriteAll(t.Context(), "src", []byte("bytes")); err != nil {
		t.Fatalf("WriteAll() error = %v", err)
	}

	copied, err := store.Copy(t.Context(), "dst", "src")
	if err != nil {
		t.Fatalf("Copy() error = %v", err)
	}
	if copied.Key != "dst" {
		t.Errorf("Copy() = %+v, want the destination", copied)
	}
	for _, key := range []string{"src", "dst"} {
		got, err := store.ReadAll(t.Context(), key)
		if err != nil || string(got) != "bytes" {
			t.Errorf("ReadAll(%q) = %q, %v", key, got, err)
		}
	}
}

func TestABodyTooBigForOneRequestGoesUpInParts(t *testing.T) {
	fake := newFakeStore()
	store := bucketFixture(t, fake)
	store.Thresholds(8, 4)

	body := bytes.Repeat([]byte("0123456789"), 3)
	w := store.NewWriter(t.Context(), "big", ocel.ContentType("application/octet-stream"))
	for at := 0; at < len(body); at += 7 {
		if _, err := w.Write(body[at:min(at+7, len(body))]); err != nil {
			t.Fatalf("Write() error = %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close() error = %v", err)
	}

	got, err := store.ReadAll(t.Context(), "big")
	if err != nil {
		t.Fatalf("ReadAll() error = %v", err)
	}
	if !bytes.Equal(got, body) {
		t.Errorf("ReadAll() = %q, want the %d bytes that were written", got, len(body))
	}
	attrs, err := store.Attrs(t.Context(), "big")
	if err != nil {
		t.Fatalf("Attrs() error = %v", err)
	}
	if attrs.ContentType != "application/octet-stream" {
		t.Errorf("Attrs().ContentType = %q, want what the writer declared", attrs.ContentType)
	}
	if len(fake.created) != 1 {
		t.Errorf("multipart uploads = %v, want the body to have gone up in parts", fake.created)
	}
	if len(fake.aborted) != 0 {
		t.Errorf("aborted = %v, want a settled upload to abort nothing", fake.aborted)
	}
}

func TestAPartTheStoreRefusesThrowsTheWholeUploadAway(t *testing.T) {
	fake := newFakeStore()
	fake.refusePart = 2
	store := bucketFixture(t, fake)
	store.Thresholds(8, 4)

	w := store.NewWriter(t.Context(), "big")
	_, write := w.Write(bytes.Repeat([]byte("x"), 30))
	err := errors.Join(write, w.Close())
	if err == nil {
		t.Fatal("the write succeeded although the store refused a part")
	}
	if !strings.Contains(err.Error(), "part 2") {
		t.Errorf("error = %q, want it to name the part the store refused", err)
	}
	if len(fake.aborted) != 1 {
		t.Errorf("aborted = %v, want the upload thrown away exactly once", fake.aborted)
	}
	if held, _ := store.Exists(t.Context(), "big"); held {
		t.Error("the bucket holds the object although no part settled")
	}
}

func TestAnAbandonedWriteThrowsItsPartsAway(t *testing.T) {
	fake := newFakeStore()
	store := bucketFixture(t, fake)
	store.Thresholds(8, 4)

	w := store.NewWriter(t.Context(), "big")
	if _, err := w.Write(bytes.Repeat([]byte("x"), 30)); err != nil {
		t.Fatalf("Write() error = %v", err)
	}
	w.Abort(errors.New("the caller gave up"))

	if err := w.Close(); err == nil || !strings.Contains(err.Error(), "the caller gave up") {
		t.Errorf("Close() error = %v, want the cause the write was abandoned with", err)
	}
	if len(fake.aborted) != 1 {
		t.Errorf("aborted = %v, want the upload thrown away exactly once", fake.aborted)
	}
}

func TestASignedUrlIsSignedForSomeoneOutside(t *testing.T) {
	fake := newFakeStore()
	store := bucketFixture(t, fake)

	got, err := store.SignedURL(t.Context(), "poster.png", ocel.Expires(5*time.Minute), ocel.Download("poster.png"))
	if err != nil {
		t.Fatalf("SignedURL() error = %v", err)
	}
	if !strings.HasSuffix(got, "/o/poster.png") {
		t.Errorf("SignedURL() = %q", got)
	}

	signed := lastSigned(t, fake)
	if signed.GetAudience() != bucketv1.SignedAudience_SIGNED_AUDIENCE_EXTERNAL {
		t.Errorf("audience = %v, want it signed for outside this deploy", signed.GetAudience())
	}
	if signed.GetOperation() != bucketv1.SignedOperation_SIGNED_OPERATION_GET {
		t.Errorf("operation = %v, want a read", signed.GetOperation())
	}
	if signed.GetBucket() != storeBucket {
		t.Errorf("bucket = %q, want the store the binding named", signed.GetBucket())
	}
	if signed.GetConstraints().GetDownloadFilename() != "poster.png" {
		t.Errorf("constraints = %v, want the download filename", signed.GetConstraints())
	}
	if signed.GetExpiresIn().AsDuration() != 5*time.Minute {
		t.Errorf("expiresIn = %v, want 5m", signed.GetExpiresIn().AsDuration())
	}
}

func TestASignedUploadCarriesTheFormItsPolicySigned(t *testing.T) {
	fake := newFakeStore()
	store := bucketFixture(t, fake)

	upload, err := store.SignedUpload(t.Context(), "inbox/one.png",
		ocel.Expires(time.Minute), ocel.MaxSize(1<<20), ocel.ContentType("image/png"))
	if err != nil {
		t.Fatalf("SignedUpload() error = %v", err)
	}
	if upload.Method != "POST" || upload.Fields["policy"] != "signed" {
		t.Errorf("SignedUpload() = %+v, want the post target the runtime signed", upload)
	}
	if upload.Headers["x-signed-by"] != "fake" {
		t.Errorf("Headers = %v, want the headers the signature covers", upload.Headers)
	}
	if upload.Expires.IsZero() {
		t.Error("Expires is zero although the caller asked for a lifetime")
	}

	signed := lastSigned(t, fake)
	if signed.GetOperation() != bucketv1.SignedOperation_SIGNED_OPERATION_POST_UPLOAD {
		t.Errorf("operation = %v, want a browser upload", signed.GetOperation())
	}
	if signed.GetConstraints().GetMaxSize() != 1<<20 || signed.GetConstraints().GetContentType() != "image/png" {
		t.Errorf("constraints = %v, want what the caller bounded the target by", signed.GetConstraints())
	}
}

func TestARefusalToSignForOutsideReachesTheCallerWorded(t *testing.T) {
	fake := newFakeStore()
	fake.externalRefusal = "this project has no domain bound, so nothing can be signed for a browser"
	store := bucketFixture(t, fake)

	_, err := store.SignedURL(t.Context(), "poster.png")
	if err == nil || !strings.Contains(err.Error(), fake.externalRefusal) {
		t.Errorf("SignedURL() error = %v, want the runtime's own wording", err)
	}

	_, err = store.SignedUpload(t.Context(), "poster.png")
	if err == nil || !strings.Contains(err.Error(), fake.externalRefusal) {
		t.Errorf("SignedUpload() error = %v, want the runtime's own wording", err)
	}
}

func TestAPublicUrlIsTheObjectUnderThePublicAddress(t *testing.T) {
	store := bucketFixture(t, newFakeStore())

	got, err := store.PublicURL("a/b c.png")
	if err != nil {
		t.Fatalf("PublicURL() error = %v", err)
	}
	if want := publicBase + "/a/b%20c.png"; got.String() != want {
		t.Errorf("PublicURL() = %q, want %q", got, want)
	}
}

func TestABucketWithNoPublicAddressSaysWhatItNeeds(t *testing.T) {
	store := privateBucketFixture(t, newFakeStore())

	_, err := store.PublicURL("a.png")
	if err == nil {
		t.Fatal("PublicURL() succeeded on a bucket with no public address")
	}
	for _, want := range []string{"a.png", "ocel.BucketPublic()", "domain"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("PublicURL() error = %q, want it to mention %q", err, want)
		}
	}
}

func TestABucketNoDeployDeliveredSaysWhatDeliversIt(t *testing.T) {
	store := ocel.Bucket("avatars")

	_, err := store.Attrs(t.Context(), "a")
	var missing *ocel.MissingBindingError
	if !errors.As(err, &missing) {
		t.Fatalf("Attrs() error = %v, want a *ocel.MissingBindingError", err)
	}
	if missing.Key != "OCEL_RESOURCE_BUCKET_avatars" {
		t.Errorf("Key = %q, want the env var the binding arrives in", missing.Key)
	}
}

func TestABucketWithNoRuntimeToReachSaysSo(t *testing.T) {
	t.Setenv("OCEL_RESOURCE_BUCKET_avatars", `{"name":"avatars","bucket":{"bucket":"b"}}`)

	_, err := ocel.Bucket("avatars").Attrs(t.Context(), "a")
	if err == nil {
		t.Fatal("Attrs() succeeded with no runtime address")
	}
	for _, want := range []string{constants.RuntimeAddressEnvName, "ocel dev", "ocel deploy"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("Attrs() error = %q, want it to mention %q", err, want)
		}
	}

	t.Setenv(constants.RuntimeAddressEnvName, "http://127.0.0.1:1")
	_, err = ocel.Bucket("avatars").Attrs(t.Context(), "a")
	if err == nil || !strings.Contains(err.Error(), channel.SessionTokenEnvVar) {
		t.Errorf("Attrs() error = %v, want it to name the token the runtime demands", err)
	}
}

func lastSigned(t *testing.T, fake *fakeStore) *bucketv1.SignRequest {
	t.Helper()
	if len(fake.signed) == 0 {
		t.Fatal("the runtime was asked to sign nothing")
	}
	return fake.signed[len(fake.signed)-1]
}
