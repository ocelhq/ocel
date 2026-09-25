package s3

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/url"
	"sort"
	"strings"
	"sync"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/pkg/constants"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

type apiError struct{ code string }

func (e *apiError) Error() string                 { return e.code }
func (e *apiError) ErrorCode() string             { return e.code }
func (e *apiError) ErrorMessage() string          { return e.code }
func (e *apiError) ErrorFault() smithy.ErrorFault { return smithy.FaultClient }

type stored struct {
	body        []byte
	contentType string
	etag        string
}

type fakeStore struct {
	mu      sync.Mutex
	objects map[string]map[string]stored
	next    int
}

func newFakeStore() *fakeStore {
	return &fakeStore{objects: map[string]map[string]stored{}}
}

func (f *fakeStore) put(bucket, key string, body []byte, contentType string) string {
	if f.objects[bucket] == nil {
		f.objects[bucket] = map[string]stored{}
	}
	f.next++
	etag := `"v` + string(rune('0'+f.next%10)) + key + `"`
	f.objects[bucket][key] = stored{body: body, contentType: contentType, etag: etag}
	return etag
}

func (f *fakeStore) HeadObject(_ context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	held, ok := f.objects[aws.ToString(in.Bucket)][aws.ToString(in.Key)]
	if !ok {
		return nil, &s3types.NotFound{}
	}
	return &s3.HeadObjectOutput{
		ContentLength: aws.Int64(int64(len(held.body))),
		ContentType:   aws.String(held.contentType),
		ETag:          aws.String(held.etag),
		LastModified:  aws.Time(time.Unix(1_700_000_000, 0).UTC()),
	}, nil
}

func (f *fakeStore) GetObject(_ context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	held, ok := f.objects[aws.ToString(in.Bucket)][aws.ToString(in.Key)]
	if !ok {
		return nil, &s3types.NoSuchKey{}
	}
	return &s3.GetObjectOutput{
		Body: io.NopCloser(bytes.NewReader(held.body)),
		ETag: aws.String(held.etag),
	}, nil
}

func (f *fakeStore) PutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	bucket, key := aws.ToString(in.Bucket), aws.ToString(in.Key)
	held, exists := f.objects[bucket][key]
	if aws.ToString(in.IfNoneMatch) == "*" && exists {
		return nil, &apiError{code: "PreconditionFailed"}
	}
	if match := aws.ToString(in.IfMatch); match != "" && (!exists || held.etag != match) {
		return nil, &apiError{code: "PreconditionFailed"}
	}
	body, err := io.ReadAll(in.Body)
	if err != nil {
		return nil, err
	}
	etag := f.put(bucket, key, body, aws.ToString(in.ContentType))
	return &s3.PutObjectOutput{ETag: aws.String(etag)}, nil
}

func (f *fakeStore) ListObjectsV2(_ context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var keys []string
	for key := range f.objects[aws.ToString(in.Bucket)] {
		if strings.HasPrefix(key, aws.ToString(in.Prefix)) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if after := aws.ToString(in.ContinuationToken); after != "" {
		for len(keys) > 0 && keys[0] <= after {
			keys = keys[1:]
		}
	}
	out := &s3.ListObjectsV2Output{}
	if limit := int(aws.ToInt32(in.MaxKeys)); limit > 0 && len(keys) > limit {
		keys = keys[:limit]
		out.NextContinuationToken = aws.String(keys[len(keys)-1])
	}
	for _, key := range keys {
		held := f.objects[aws.ToString(in.Bucket)][key]
		out.Contents = append(out.Contents, s3types.Object{
			Key:  aws.String(key),
			Size: aws.Int64(int64(len(held.body))),
			ETag: aws.String(held.etag),
		})
	}
	return out, nil
}

func (f *fakeStore) DeleteObjects(_ context.Context, in *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	for _, id := range in.Delete.Objects {
		delete(f.objects[aws.ToString(in.Bucket)], aws.ToString(id.Key))
	}
	return &s3.DeleteObjectsOutput{}, nil
}

func (f *fakeStore) CopyObject(_ context.Context, in *s3.CopyObjectInput, _ ...func(*s3.Options)) (*s3.CopyObjectOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	source, err := url.PathUnescape(aws.ToString(in.CopySource))
	if err != nil {
		return nil, err
	}
	_, key, _ := strings.Cut(strings.TrimPrefix(source, "/"), "/")
	held, ok := f.objects[aws.ToString(in.Bucket)][key]
	if !ok {
		return nil, &s3types.NoSuchKey{}
	}
	f.put(aws.ToString(in.Bucket), aws.ToString(in.Key), held.body, held.contentType)
	return &s3.CopyObjectOutput{}, nil
}

func (f *fakeStore) CreateMultipartUpload(_ context.Context, in *s3.CreateMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	return &s3.CreateMultipartUploadOutput{UploadId: aws.String("upload-1")}, nil
}

func (f *fakeStore) ListMultipartUploads(_ context.Context, _ *s3.ListMultipartUploadsInput, _ ...func(*s3.Options)) (*s3.ListMultipartUploadsOutput, error) {
	return &s3.ListMultipartUploadsOutput{}, nil
}

func (f *fakeStore) CompleteMultipartUpload(_ context.Context, in *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.put(aws.ToString(in.Bucket), aws.ToString(in.Key), []byte("assembled"), "application/octet-stream")
	return &s3.CompleteMultipartUploadOutput{}, nil
}

func (f *fakeStore) AbortMultipartUpload(_ context.Context, in *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	return &s3.AbortMultipartUploadOutput{}, nil
}

type recordingPoster struct {
	mu    sync.Mutex
	posts []callbackBody
}

func (p *recordingPoster) Post(_ context.Context, _ string, body []byte) error {
	p.mu.Lock()
	defer p.mu.Unlock()
	var held callbackBody
	if err := json.Unmarshal(body, &held); err != nil {
		return err
	}
	p.posts = append(p.posts, held)
	return nil
}

func presigner() *s3.PresignClient {
	return s3.NewPresignClient(s3.New(s3.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", ""),
		BaseEndpoint: aws.String("http://rustfs:9000"),
		UsePathStyle: true,
	}))
}

func externalPresigner() *s3.PresignClient {
	return s3.NewPresignClient(s3.New(s3.Options{
		Region:       "us-east-1",
		Credentials:  credentials.NewStaticCredentialsProvider("AKIDEXAMPLE", "secret", ""),
		BaseEndpoint: aws.String("https://storage.example.com"),
		UsePathStyle: true,
	}))
}

type harness struct {
	svc    *Service
	store  *fakeStore
	poster *recordingPoster
	now    time.Time
}

func newHarness(t *testing.T, tweak func(*Config)) *harness {
	t.Helper()
	store := newFakeStore()
	poster := &recordingPoster{}
	cfg := Config{
		Objects:  store,
		Internal: presigner(),
		External: func(context.Context) (PresignAPI, string) {
			return externalPresigner(), "https://storage.example.com"
		},
		Callbacks:    poster,
		Sessions:     "store",
		Granted:      []string{"store"},
		PostPolicies: true,
	}
	if tweak != nil {
		tweak(&cfg)
	}
	h := &harness{store: store, poster: poster, now: time.Unix(1_000_000, 0)}
	h.svc = New(cfg)
	h.svc.now = func() time.Time { return h.now }
	h.svc.newID = func() string { return "sess_fixed" }
	h.svc.newSecret = func() string { return "test-secret" }
	return h
}

func (h *harness) presign(t *testing.T, key string, size int64, mime string) *bucketv1.PresignUploadResponse {
	t.Helper()
	resp, err := h.svc.PresignUpload(context.Background(), &bucketv1.PresignUploadRequest{
		Bucket:          "store",
		CallbackBaseUrl: "http://127.0.0.1:3000/api/upload",
		Metadata:        []byte(`{"uploader":"avatar"}`),
		Files:           []*bucketv1.PresignFile{{Key: key, Name: key, Size: size, MimeType: mime}},
	})
	if err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}
	return resp
}

func TestASessionLivesInTheStoreItGuards(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	h.presign(t, "avatars/a.png", 3, "image/png")

	if _, ok := h.store.objects["store"][sessionPrefix+"sess_fixed"]; !ok {
		t.Fatalf("the session is not in the store: %v", h.store.objects["store"])
	}
	if _, err := h.svc.List(context.Background(), &bucketv1.ListRequest{Bucket: "store"}); err != nil {
		t.Fatalf("List: %v", err)
	}
	listed, _ := h.svc.List(context.Background(), &bucketv1.ListRequest{Bucket: "store"})
	for _, obj := range listed.GetObjects() {
		if strings.HasPrefix(obj.GetKey(), constants.ReservedKeyPrefix) {
			t.Fatalf("List returned %q, and the store's own bookkeeping is not the app's to see", obj.GetKey())
		}
	}
}

func TestTwoReplicasCannotOpenTheSameSession(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	h.presign(t, "a.png", 3, "image/png")
	_, err := h.svc.PresignUpload(context.Background(), &bucketv1.PresignUploadRequest{
		Bucket: "store",
		Files:  []*bucketv1.PresignFile{{Key: "b.png", Name: "b.png", Size: 3, MimeType: "image/png"}},
	})

	if err == nil {
		t.Fatal("a second session took the same id and overwrote the first")
	}
}

func TestCompleteUploadConfirmsWhatTheStoreActuallyHolds(t *testing.T) {
	t.Parallel()

	t.Run("an object that is not there yet leaves the session pending", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, nil)
		h.presign(t, "a.png", 3, "image/png")

		resp, err := h.svc.CompleteUpload(context.Background(), &bucketv1.CompleteUploadRequest{SessionId: "sess_fixed"})
		if err != nil {
			t.Fatalf("CompleteUpload: %v", err)
		}
		if resp.GetState() != bucketv1.UploadState_UPLOAD_STATE_PENDING {
			t.Fatalf("state = %v, want PENDING", resp.GetState())
		}
		if len(h.poster.posts) != 0 {
			t.Fatalf("the app was told about an upload that never landed: %+v", h.poster.posts)
		}
	})

	t.Run("an object matching what was signed settles the session and tells the app once", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, nil)
		h.presign(t, "a.png", 3, "image/png")
		h.store.put("store", "a.png", []byte("abc"), "image/png")

		resp, err := h.svc.CompleteUpload(context.Background(), &bucketv1.CompleteUploadRequest{SessionId: "sess_fixed"})
		if err != nil {
			t.Fatalf("CompleteUpload: %v", err)
		}
		if resp.GetState() != bucketv1.UploadState_UPLOAD_STATE_SUCCEEDED {
			t.Fatalf("state = %v, want SUCCEEDED", resp.GetState())
		}
		if len(h.poster.posts) != 1 || h.poster.posts[0].File.Key != "a.png" {
			t.Fatalf("callbacks = %+v, want one naming the uploaded key", h.poster.posts)
		}

		if _, err := h.svc.CompleteUpload(context.Background(), &bucketv1.CompleteUploadRequest{SessionId: "sess_fixed"}); err != nil {
			t.Fatalf("CompleteUpload again: %v", err)
		}
		if len(h.poster.posts) != 1 {
			t.Fatalf("the app was told %d times, want once however often the client confirms", len(h.poster.posts))
		}
	})

	t.Run("an object that is not the one signed for is deleted and the session fails", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, nil)
		h.presign(t, "a.png", 3, "image/png")
		h.store.put("store", "a.png", []byte("a much larger body than was signed for"), "application/zip")

		resp, err := h.svc.CompleteUpload(context.Background(), &bucketv1.CompleteUploadRequest{SessionId: "sess_fixed"})
		if err != nil {
			t.Fatalf("CompleteUpload: %v", err)
		}
		if resp.GetState() != bucketv1.UploadState_UPLOAD_STATE_EXPIRED || resp.GetError() == "" {
			t.Fatalf("resp = %+v, want the session refused with a reason", resp)
		}
		if _, held := h.store.objects["store"]["a.png"]; held {
			t.Fatal("the object nobody signed for is still in the store")
		}
		if len(h.poster.posts) != 0 {
			t.Fatalf("the app was told about an object it never authorised: %+v", h.poster.posts)
		}
	})

	t.Run("an expired session takes its unconfirmed objects with it", func(t *testing.T) {
		t.Parallel()
		h := newHarness(t, nil)
		h.presign(t, "a.png", 3, "image/png")
		h.store.put("store", "a.png", []byte("abc"), "image/png")
		h.now = h.now.Add(3 * time.Hour)

		resp, err := h.svc.CompleteUpload(context.Background(), &bucketv1.CompleteUploadRequest{SessionId: "sess_fixed"})
		if err != nil {
			t.Fatalf("CompleteUpload: %v", err)
		}
		if resp.GetState() != bucketv1.UploadState_UPLOAD_STATE_EXPIRED {
			t.Fatalf("state = %v, want EXPIRED", resp.GetState())
		}
		if _, held := h.store.objects["store"]["a.png"]; held {
			t.Fatal("an expired session left its object behind, and nothing will ever claim it")
		}
	})
}

func TestTheCallbackCarriesASignatureTheServiceItselfVerifies(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)
	h.presign(t, "a.png", 3, "image/png")
	h.store.put("store", "a.png", []byte("abc"), "image/png")
	if _, err := h.svc.CompleteUpload(context.Background(), &bucketv1.CompleteUploadRequest{SessionId: "sess_fixed"}); err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	posted := h.poster.posts[0]

	got, err := h.svc.VerifyUploadSignature(context.Background(), &bucketv1.VerifyUploadSignatureRequest{
		SessionId: posted.SessionID,
		Signature: posted.Signature,
		File: &bucketv1.CompletedFile{
			Key: posted.File.Key, Name: posted.File.Name, Size: posted.File.Size, MimeType: posted.File.MimeType,
		},
	})
	if err != nil || !got.GetValid() || string(got.GetMetadata()) != `{"uploader":"avatar"}` {
		t.Fatalf("VerifyUploadSignature = %+v, %v, want the session's metadata back", got, err)
	}

	forged, err := h.svc.VerifyUploadSignature(context.Background(), &bucketv1.VerifyUploadSignatureRequest{
		SessionId: posted.SessionID,
		Signature: "deadbeef",
		File:      &bucketv1.CompletedFile{Key: posted.File.Key, Name: posted.File.Name, Size: posted.File.Size, MimeType: posted.File.MimeType},
	})
	if err != nil || forged.GetValid() || forged.GetMetadata() != nil {
		t.Fatalf("a forged signature verified: %+v", forged)
	}
}

func TestSigningForABrowserNeedsAPublicAddress(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(cfg *Config) {
		cfg.External = nil
	})

	_, err := h.svc.Sign(context.Background(), &bucketv1.SignRequest{
		Bucket:    "store",
		Key:       "a.png",
		Operation: bucketv1.SignedOperation_SIGNED_OPERATION_GET,
		Audience:  bucketv1.SignedAudience_SIGNED_AUDIENCE_EXTERNAL,
	})

	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("Sign for a browser err = %v, want it refused", err)
	}
	if !strings.Contains(err.Error(), "domain") {
		t.Fatalf("Sign err = %v, want it to name the missing domain", err)
	}

	if _, err := h.svc.Sign(context.Background(), &bucketv1.SignRequest{
		Bucket:    "store",
		Key:       "a.png",
		Operation: bucketv1.SignedOperation_SIGNED_OPERATION_GET,
		Audience:  bucketv1.SignedAudience_SIGNED_AUDIENCE_INTERNAL,
	}); err != nil {
		t.Fatalf("Sign for the app itself = %v, want it signed against the store's internal address", err)
	}
}

func TestSignedUrlsAddressTheAudienceTheyAreFor(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	internal, err := h.svc.Sign(context.Background(), &bucketv1.SignRequest{
		Bucket: "store", Key: "a.png",
		Operation: bucketv1.SignedOperation_SIGNED_OPERATION_GET,
		Audience:  bucketv1.SignedAudience_SIGNED_AUDIENCE_INTERNAL,
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}
	external, err := h.svc.Sign(context.Background(), &bucketv1.SignRequest{
		Bucket: "store", Key: "a.png",
		Operation: bucketv1.SignedOperation_SIGNED_OPERATION_GET,
		Audience:  bucketv1.SignedAudience_SIGNED_AUDIENCE_EXTERNAL,
	})
	if err != nil {
		t.Fatalf("Sign: %v", err)
	}

	if !strings.Contains(internal.GetTarget().GetUrl(), "rustfs:9000") {
		t.Fatalf("the app's own url = %q, want the store's internal address", internal.GetTarget().GetUrl())
	}
	if !strings.Contains(external.GetTarget().GetUrl(), "storage.example.com") {
		t.Fatalf("a browser's url = %q, want the store's public address", external.GetTarget().GetUrl())
	}
}

func TestAnExternalStoreNeverShowsItsPrefix(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(cfg *Config) {
		cfg.Sessions = "shared/shop/prod/web"
		cfg.Granted = []string{"shared/shop/prod/uploads"}
	})
	ctx := context.Background()
	const spec = "shared/shop/prod/uploads"

	if _, err := h.svc.Copy(ctx, &bucketv1.CopyRequest{Bucket: spec, SourceKey: "a.png", DestinationKey: "b.png"}); err == nil {
		t.Fatal("copying a source that is not there succeeded")
	}
	h.store.put("shared", "shop/prod/uploads/a.png", []byte("abc"), "image/png")

	head, err := h.svc.Head(ctx, &bucketv1.HeadRequest{Bucket: spec, Key: "a.png"})
	if err != nil {
		t.Fatalf("Head: %v", err)
	}
	if head.GetObject().GetKey() != "a.png" {
		t.Fatalf("Head = %q, want the key the app used, not the one the store holds", head.GetObject().GetKey())
	}

	listed, err := h.svc.List(ctx, &bucketv1.ListRequest{Bucket: spec})
	if err != nil {
		t.Fatalf("List: %v", err)
	}
	if len(listed.GetObjects()) != 1 || listed.GetObjects()[0].GetKey() != "a.png" {
		t.Fatalf("List = %+v, want the one object under this resource's prefix, unprefixed", listed.GetObjects())
	}

	if _, err := h.svc.Delete(ctx, &bucketv1.DeleteRequest{Bucket: spec, Keys: []string{"a.png"}}); err != nil {
		t.Fatalf("Delete: %v", err)
	}
	if _, held := h.store.objects["shared"]["shop/prod/uploads/a.png"]; held {
		t.Fatal("the delete did not reach the object under this resource's prefix")
	}
}

func TestAStoreWithNoRoomSignsNoWrite(t *testing.T) {
	t.Parallel()

	full := newHarness(t, func(cfg *Config) {
		cfg.Volume = func() (uint64, uint64, error) { return 200 << 20, 100 << 30, nil }
	})
	roomy := newHarness(t, func(cfg *Config) {
		cfg.Volume = func() (uint64, uint64, error) { return 50 << 30, 100 << 30, nil }
	})

	write := &bucketv1.SignRequest{
		Bucket: "store", Key: "a.png",
		Operation: bucketv1.SignedOperation_SIGNED_OPERATION_PUT,
		Audience:  bucketv1.SignedAudience_SIGNED_AUDIENCE_INTERNAL,
	}
	read := &bucketv1.SignRequest{
		Bucket: "store", Key: "a.png",
		Operation: bucketv1.SignedOperation_SIGNED_OPERATION_GET,
		Audience:  bucketv1.SignedAudience_SIGNED_AUDIENCE_INTERNAL,
	}

	_, err := full.svc.Sign(context.Background(), write)
	var connectErr *connect.Error
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeResourceExhausted {
		t.Fatalf("Sign a write against a full volume = %v, want it refused", err)
	}
	if !strings.Contains(err.Error(), "free") {
		t.Fatalf("Sign err = %v, want it to say what is wrong", err)
	}
	if _, err := full.svc.Sign(context.Background(), read); err != nil {
		t.Fatalf("Sign a read against a full volume = %v, want reads to keep working", err)
	}
	if _, err := roomy.svc.Sign(context.Background(), write); err != nil {
		t.Fatalf("Sign a write against a volume with room = %v, want it signed", err)
	}

	parts := &bucketv1.SignPartsRequest{
		Bucket: "store", Key: "a.png", UploadId: "upload-1",
		PartNumbers: []int32{1}, Audience: bucketv1.SignedAudience_SIGNED_AUDIENCE_INTERNAL,
	}
	_, err = full.svc.SignParts(context.Background(), parts)
	if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeResourceExhausted {
		t.Fatalf("SignParts against a full volume = %v, want it refused: a part carries the same bytes a put would", err)
	}
	if _, err := roomy.svc.SignParts(context.Background(), parts); err != nil {
		t.Fatalf("SignParts against a volume with room = %v, want it signed", err)
	}
}

func TestAStoreThatSignsNoPolicyStillBoundsAnUploadByHead(t *testing.T) {
	t.Parallel()
	h := newHarness(t, func(cfg *Config) { cfg.PostPolicies = false })

	resp := h.presign(t, "a.png", 3, "image/png")

	target := resp.GetFiles()[0]
	if target.GetMethod() != "PUT" {
		t.Fatalf("method = %q, want a PUT where the store signs no POST policy", target.GetMethod())
	}
	h.store.put("store", "a.png", []byte("much longer than three bytes"), "image/png")

	settled, err := h.svc.CompleteUpload(context.Background(), &bucketv1.CompleteUploadRequest{SessionId: "sess_fixed"})
	if err != nil {
		t.Fatalf("CompleteUpload: %v", err)
	}
	if settled.GetState() != bucketv1.UploadState_UPLOAD_STATE_EXPIRED {
		t.Fatalf("state = %v, want the oversized upload refused", settled.GetState())
	}
}

func TestABrowserUploadIsBoundedByPolicyWhereTheStoreSignsOne(t *testing.T) {
	t.Parallel()
	h := newHarness(t, nil)

	resp := h.presign(t, "a.png", 3, "image/png")

	target := resp.GetFiles()[0]
	if target.GetMethod() != "POST" {
		t.Fatalf("method = %q, want POST", target.GetMethod())
	}
	if target.GetFields()["policy"] == "" {
		t.Fatalf("fields = %v, want the policy that bounds the body before a byte is sent", target.GetFields())
	}
	if got := target.GetFields()["Content-Type"]; got != "image/png" {
		t.Fatalf("fields carry Content-Type %q, and a policy conditioned on one refuses a form that omits it", got)
	}
}
