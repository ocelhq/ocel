package bucket

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

	bucketsv1 "github.com/ocelhq/ocel/pkg/proto/buckets/v1"
)

const (
	testBucket   = "prod-bucket"
	testKey      = "avatars/u1.png"
	testOrigin   = "https://app.example.com"
	testCallback = testOrigin + "/api/upload"
)

type fakeTagger struct {
	tags map[string]string
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

func seedPendingSession(t *testing.T, ddb *fakeDDB, callbackURL string) (id, secret string) {
	t.Helper()
	id = "sess_test1"
	secret = "supersecret"
	store := &sessionStore{client: ddb, table: "sessions", keyPrefix: testSessionKeyPrefix}
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

func newUploadCompleter(ddb *fakeDDB, tagger objectTagger, doer httpDoer, origins []string) *UploadCompleter {
	return NewUploadCompleter(UploadCompleterConfig{
		DDB:              ddb,
		Tagger:           tagger,
		HTTP:             doer,
		Table:            "sessions",
		SessionKeyPrefix: testSessionKeyPrefix,
		AllowedOrigins:   origins,
	})
}

func TestUploadCompleter(t *testing.T) {
	t.Parallel()

	t.Run("transitions once and signs the accepted callback", func(t *testing.T) {
		t.Parallel()
		ddb := newFakeDDB()
		id, secret := seedPendingSession(t, ddb, testCallback)
		doer := &recordingDoer{}

		l := newUploadCompleter(ddb, &fakeTagger{tags: map[string]string{sessionTagKey: id}}, doer, []string{testOrigin})
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

		svc := &Service{store: &sessionStore{client: ddb, table: "sessions", keyPrefix: testSessionKeyPrefix}}
		resp, err := svc.VerifyUploadSignature(context.Background(), verifyReq(id, post.body))
		if err != nil {
			t.Fatalf("VerifyUploadSignature: %v", err)
		}
		if !resp.GetValid() {
			t.Fatal("genuine upload-completer signature was rejected by VerifyUploadSignature")
		}

		want := mustSign(t, secret, id, SignedFile{Key: testKey, Name: "u1.png", Size: 2048, MimeType: "image/png"})
		if post.body.Signature != want {
			t.Fatalf("signature = %q, want %q", post.body.Signature, want)
		}
	})

	t.Run("a forged signature is rejected", func(t *testing.T) {
		t.Parallel()
		ddb := newFakeDDB()
		id, _ := seedPendingSession(t, ddb, testCallback)
		doer := &recordingDoer{}

		l := newUploadCompleter(ddb, &fakeTagger{tags: map[string]string{sessionTagKey: id}}, doer, []string{testOrigin})
		if err := l.Handle(context.Background(), objectCreatedEvent(testBucket, testKey)); err != nil {
			t.Fatalf("Handle: %v", err)
		}

		forged := doer.posts[0].body
		forged.Signature = forged.Signature[:len(forged.Signature)-1] + "0"

		svc := &Service{store: &sessionStore{client: ddb, table: "sessions", keyPrefix: testSessionKeyPrefix}}
		resp, err := svc.VerifyUploadSignature(context.Background(), verifyReq(id, forged))
		if err != nil {
			t.Fatalf("VerifyUploadSignature: %v", err)
		}
		if resp.GetValid() {
			t.Fatal("a tampered signature was accepted; blast radius must stay one upload")
		}
	})

	t.Run("a duplicate event no-ops", func(t *testing.T) {
		t.Parallel()
		ddb := newFakeDDB()
		id, _ := seedPendingSession(t, ddb, testCallback)
		doer := &recordingDoer{}

		l := newUploadCompleter(ddb, &fakeTagger{tags: map[string]string{sessionTagKey: id}}, doer, []string{testOrigin})
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
	})

	t.Run("a callback target that is not allowlisted is never posted to", func(t *testing.T) {
		t.Parallel()
		ddb := newFakeDDB()
		id, _ := seedPendingSession(t, ddb, "https://evil.example.com/api/upload")
		doer := &recordingDoer{}

		l := newUploadCompleter(ddb, &fakeTagger{tags: map[string]string{sessionTagKey: id}}, doer, []string{testOrigin})
		if err := l.Handle(context.Background(), objectCreatedEvent(testBucket, testKey)); err != nil {
			t.Fatalf("Handle: %v", err)
		}

		if len(doer.posts) != 0 {
			t.Fatalf("callbacks fired = %d, want 0 (target not allowlisted)", len(doer.posts))
		}
	})

	t.Run("an untagged object no-ops", func(t *testing.T) {
		t.Parallel()
		ddb := newFakeDDB()
		seedPendingSession(t, ddb, testCallback)
		doer := &recordingDoer{}

		l := newUploadCompleter(ddb, &fakeTagger{tags: map[string]string{}}, doer, []string{testOrigin})
		if err := l.Handle(context.Background(), objectCreatedEvent(testBucket, testKey)); err != nil {
			t.Fatalf("Handle: %v", err)
		}
		if len(doer.posts) != 0 {
			t.Fatalf("callbacks fired = %d, want 0 (untagged object)", len(doer.posts))
		}
	})
}

func TestOriginAllowed(t *testing.T) {
	t.Parallel()

	allowed := []string{"https://app.example.com", "https://www.example.com"}
	cases := []struct {
		name string
		url  string
		want bool
	}{
		{"an allowlisted origin", "https://app.example.com/api/upload", true},
		{"another allowlisted origin", "https://www.example.com/x", true},
		{"a different host", "https://evil.example.com/api/upload", false},
		{"the same host over plain http", "http://app.example.com/api/upload", false},
		{"the same host on another port", "https://app.example.com:8443/x", false},
		{"not a url at all", "not-a-url", false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			if got := originAllowed(tc.url, allowed); got != tc.want {
				t.Errorf("originAllowed(%q) = %v, want %v", tc.url, got, tc.want)
			}
		})
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

func verifyReq(sessionID string, c signedCompletion) *bucketsv1.VerifyUploadSignatureRequest {
	return &bucketsv1.VerifyUploadSignatureRequest{
		SessionId: sessionID,
		Signature: c.Signature,
		File: &bucketsv1.CompletedFile{
			Key:      c.File.Key,
			Name:     c.File.Name,
			Size:     c.File.Size,
			MimeType: c.File.MimeType,
		},
	}
}
