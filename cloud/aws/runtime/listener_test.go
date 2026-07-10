package runtime

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	runtimev1 "github.com/ocelhq/ocel/pkg/proto/runtime/v1"
)

const (
	testBucket   = "prod-bucket"
	testKey      = "avatars/u1.png"
	testOrigin   = "https://app.example.com"
	testCallback = testOrigin + "/api/upload"
)

// fakeTagger returns a fixed tag set for any object, letting a test bind an
// object to a session id (or to none).
type fakeTagger struct {
	tags map[string]string // tag key -> value
	err  error
}

func (t *fakeTagger) GetObjectTagging(_ context.Context, _ *s3.GetObjectTaggingInput, _ ...func(*s3.Options)) (*s3.GetObjectTaggingOutput, error) {
	if t.err != nil {
		return nil, t.err
	}
	var set []s3types.Tag
	for k, v := range t.tags {
		set = append(set, s3types.Tag{Key: aws.String(k), Value: aws.String(v)})
	}
	return &s3.GetObjectTaggingOutput{TagSet: set}, nil
}

// recordingDoer records every callback POST it receives so the test can assert
// how many fired and inspect their bodies.
type recordingDoer struct {
	posts  []recordedPost
	status int
}

type recordedPost struct {
	url  string
	body signedCompletion
}

func (d *recordingDoer) Do(req *http.Request) (*http.Response, error) {
	var body signedCompletion
	if req.Body != nil {
		_ = json.NewDecoder(req.Body).Decode(&body)
	}
	d.posts = append(d.posts, recordedPost{url: req.URL.String(), body: body})
	status := d.status
	if status == 0 {
		status = http.StatusOK
	}
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("{}"))}, nil
}

// seedPendingSession writes a one-file pending session to the fake store and
// returns its id and secret.
func seedPendingSession(t *testing.T, ddb *fakeDDB, callbackURL string) (id, secret string) {
	t.Helper()
	id = "sess_test1"
	secret = "supersecret"
	store := &sessionStore{client: ddb, table: "sessions"}
	err := store.put(context.Background(), session{
		SessionID:       id,
		Secret:          secret,
		Bucket:          testBucket,
		CallbackBaseURL: callbackURL,
		Metadata:        []byte(`{"user":"u1"}`),
		Files: []sessionFile{
			{Key: testKey, Name: "u1.png", Size: 2048, MimeType: "image/png", State: statePending},
		},
		CreatedAt: 1000,
		ExpiresAt: 100000,
	})
	if err != nil {
		t.Fatalf("seed session: %v", err)
	}
	return id, secret
}

func objectCreatedEvent(bucket, key string) S3Event {
	return S3Event{Records: []S3EventRecord{{S3: S3Entity{
		Bucket: S3Bucket{Name: bucket},
		Object: S3Object{Key: key},
	}}}}
}

func newListener(ddb *fakeDDB, tagger objectTagger, doer httpDoer, origins []string) *Listener {
	return NewListener(ListenerConfig{
		DDB:            ddb,
		Tagger:         tagger,
		HTTP:           doer,
		Table:          "sessions",
		AllowedOrigins: origins,
	})
}

// TestListener_TransitionsOnceAndSignsAcceptedCallback proves the happy path:
// one ObjectCreated event transitions the file, and the emitted op=callback
// carries a genuine per-session signature that VerifyUploadSignature accepts.
func TestListener_TransitionsOnceAndSignsAcceptedCallback(t *testing.T) {
	ddb := newFakeDDB()
	id, secret := seedPendingSession(t, ddb, testCallback)
	tagger := &fakeTagger{tags: map[string]string{sessionTagKey: id}}
	doer := &recordingDoer{}

	l := newListener(ddb, tagger, doer, []string{testOrigin})
	if err := l.Handle(context.Background(), objectCreatedEvent(testBucket, testKey)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(doer.posts) != 1 {
		t.Fatalf("callbacks fired = %d, want 1", len(doer.posts))
	}
	post := doer.posts[0]
	if got := queryOp(t, post.url); got != "callback" {
		t.Fatalf("callback url op = %q, want callback (url %q)", got, post.url)
	}

	// The signature the listener emitted is verified by the same runtime path the
	// env-blind route uses (VerifyUploadSignature), against the stored session.
	svc := &Service{store: &sessionStore{client: ddb, table: "sessions"}}
	resp, err := svc.VerifyUploadSignature(context.Background(), verifyReq(id, post.body))
	if err != nil {
		t.Fatalf("VerifyUploadSignature: %v", err)
	}
	if !resp.GetValid() {
		t.Fatal("genuine listener signature was rejected by VerifyUploadSignature")
	}

	// Cross-check the raw HMAC too: the emitted signature is exactly
	// HMAC(secret, canonical({sessionId, file})).
	want := signUpload(secret, id, SignedFile{Key: testKey, Name: "u1.png", Size: 2048, MimeType: "image/png"})
	if post.body.Signature != want {
		t.Fatalf("signature = %q, want %q", post.body.Signature, want)
	}
}

// TestListener_ForgedSignatureRejected proves the blast radius of a leaked or
// tampered signature is one upload: a tampered signature does not verify.
func TestListener_ForgedSignatureRejected(t *testing.T) {
	ddb := newFakeDDB()
	id, _ := seedPendingSession(t, ddb, testCallback)
	tagger := &fakeTagger{tags: map[string]string{sessionTagKey: id}}
	doer := &recordingDoer{}

	l := newListener(ddb, tagger, doer, []string{testOrigin})
	if err := l.Handle(context.Background(), objectCreatedEvent(testBucket, testKey)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	forged := doer.posts[0].body
	forged.Signature = forged.Signature[:len(forged.Signature)-1] + "0" // flip last hex nibble

	svc := &Service{store: &sessionStore{client: ddb, table: "sessions"}}
	resp, err := svc.VerifyUploadSignature(context.Background(), verifyReq(id, forged))
	if err != nil {
		t.Fatalf("VerifyUploadSignature: %v", err)
	}
	if resp.GetValid() {
		t.Fatal("a tampered signature was accepted; blast radius must stay one upload")
	}
}

// TestListener_DuplicateEventNoOps proves idempotency: a second identical
// ObjectCreated delivery does not re-transition and does not fire a second
// callback.
func TestListener_DuplicateEventNoOps(t *testing.T) {
	ddb := newFakeDDB()
	id, _ := seedPendingSession(t, ddb, testCallback)
	tagger := &fakeTagger{tags: map[string]string{sessionTagKey: id}}
	doer := &recordingDoer{}

	l := newListener(ddb, tagger, doer, []string{testOrigin})
	evt := objectCreatedEvent(testBucket, testKey)
	if err := l.Handle(context.Background(), evt); err != nil {
		t.Fatalf("first Handle: %v", err)
	}
	if err := l.Handle(context.Background(), evt); err != nil {
		t.Fatalf("second Handle: %v", err)
	}

	if len(doer.posts) != 1 {
		t.Fatalf("callbacks fired = %d, want exactly 1 (duplicate delivery must no-op)", len(doer.posts))
	}
}

// TestListener_CallbackTargetNotAllowlistedRejected proves the prod
// callback-trust check: a session whose callback_base_url origin is not in the
// deploy-known allowedOrigins gets no callback, even though the file
// transitions.
func TestListener_CallbackTargetNotAllowlistedRejected(t *testing.T) {
	ddb := newFakeDDB()
	id, _ := seedPendingSession(t, ddb, "https://evil.example.com/api/upload")
	tagger := &fakeTagger{tags: map[string]string{sessionTagKey: id}}
	doer := &recordingDoer{}

	l := newListener(ddb, tagger, doer, []string{testOrigin})
	if err := l.Handle(context.Background(), objectCreatedEvent(testBucket, testKey)); err != nil {
		t.Fatalf("Handle: %v", err)
	}

	if len(doer.posts) != 0 {
		t.Fatalf("callbacks fired = %d, want 0 (target not allowlisted)", len(doer.posts))
	}
}

// TestListener_UntaggedObjectNoOps proves an object without the sessionId tag
// (not one of ours) is ignored.
func TestListener_UntaggedObjectNoOps(t *testing.T) {
	ddb := newFakeDDB()
	seedPendingSession(t, ddb, testCallback)
	tagger := &fakeTagger{tags: map[string]string{}} // no sessionId tag
	doer := &recordingDoer{}

	l := newListener(ddb, tagger, doer, []string{testOrigin})
	if err := l.Handle(context.Background(), objectCreatedEvent(testBucket, testKey)); err != nil {
		t.Fatalf("Handle: %v", err)
	}
	if len(doer.posts) != 0 {
		t.Fatalf("callbacks fired = %d, want 0 (untagged object)", len(doer.posts))
	}
}

// TestOriginAllowed covers the origin match directly, including a spoofed host.
func TestOriginAllowed(t *testing.T) {
	allowed := []string{"https://app.example.com", "https://www.example.com"}
	cases := []struct {
		url  string
		want bool
	}{
		{"https://app.example.com/api/upload", true},
		{"https://www.example.com/x", true},
		{"https://evil.example.com/api/upload", false},
		{"http://app.example.com/api/upload", false}, // scheme mismatch
		{"https://app.example.com:8443/x", false},    // port mismatch
		{"not-a-url", false},
	}
	for _, c := range cases {
		if got := originAllowed(c.url, allowed); got != c.want {
			t.Errorf("originAllowed(%q) = %v, want %v", c.url, got, c.want)
		}
	}
}

func queryOp(t *testing.T, rawURL string) string {
	t.Helper()
	u, err := url.Parse(rawURL)
	if err != nil {
		t.Fatalf("parse callback url %q: %v", rawURL, err)
	}
	return u.Query().Get("op")
}

// verifyReq rebuilds the proto VerifyUploadSignature request from a recorded
// callback body, so the test verifies exactly what the listener put on the wire.
func verifyReq(sessionID string, c signedCompletion) *runtimev1.VerifyUploadSignatureRequest {
	return &runtimev1.VerifyUploadSignatureRequest{
		SessionId: sessionID,
		Signature: c.Signature,
		File: &runtimev1.CompletedFile{
			Key:      c.File.Key,
			Name:     c.File.Name,
			Size:     c.File.Size,
			MimeType: c.File.MimeType,
		},
	}
}
