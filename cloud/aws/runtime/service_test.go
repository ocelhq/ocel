package runtime

import (
	"context"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	runtimev1 "github.com/ocelhq/ocel/pkg/proto/runtime/v1"
)

// fakeDDB is an in-memory stand-in for the narrowed DynamoDB API, keyed by the
// session_id partition key.
type fakeDDB struct {
	items map[string]map[string]ddbtypes.AttributeValue
}

func newFakeDDB() *fakeDDB {
	return &fakeDDB{items: map[string]map[string]ddbtypes.AttributeValue{}}
}

func avString(v ddbtypes.AttributeValue) string {
	return v.(*ddbtypes.AttributeValueMemberS).Value
}

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.items[avString(in.Item["session_id"])] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	item, ok := f.items[avString(in.Key["session_id"])]
	if !ok {
		return &dynamodb.GetItemOutput{}, nil
	}
	return &dynamodb.GetItemOutput{Item: item}, nil
}

// updateFileIdxRe extracts the file index from the store's per-file transition
// UpdateExpression ("SET files[<idx>].#st = :succeeded").
var updateFileIdxRe = regexp.MustCompile(`files\[(\d+)\]`)

// UpdateItem models exactly the store's guarded pending->succeeded transition:
// it flips files[idx].state to succeeded only when it is currently pending,
// returning a ConditionalCheckFailedException otherwise. That reproduces
// DynamoDB's atomic conditional write, which is the listener's idempotency
// guarantee under duplicate S3 deliveries.
func (f *fakeDDB) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	item, ok := f.items[avString(in.Key["session_id"])]
	if !ok {
		return nil, &ddbtypes.ConditionalCheckFailedException{}
	}
	m := updateFileIdxRe.FindStringSubmatch(aws.ToString(in.UpdateExpression))
	if m == nil {
		return nil, fmt.Errorf("fakeDDB: unsupported UpdateExpression %q", aws.ToString(in.UpdateExpression))
	}
	idx, _ := strconv.Atoi(m[1])
	files, ok := item["files"].(*ddbtypes.AttributeValueMemberL)
	if !ok || idx >= len(files.Value) {
		return nil, &ddbtypes.ConditionalCheckFailedException{}
	}
	fileM := files.Value[idx].(*ddbtypes.AttributeValueMemberM).Value
	if avString(fileM["state"]) != string(statePending) {
		return nil, &ddbtypes.ConditionalCheckFailedException{}
	}
	fileM["state"] = &ddbtypes.AttributeValueMemberS{Value: string(stateSucceeded)}
	return &dynamodb.UpdateItemOutput{}, nil
}

// fakePresigner returns a deterministic URL that encodes the inputs the test
// asserts on (bucket, key, content-type/length, session tag).
type fakePresigner struct{ lastInput *s3.PutObjectInput }

func (p *fakePresigner) PresignPutObject(_ context.Context, in *s3.PutObjectInput, _ ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error) {
	p.lastInput = in
	q := url.Values{}
	q.Set("ct", aws.ToString(in.ContentType))
	q.Set("cl", strconv.FormatInt(aws.ToInt64(in.ContentLength), 10))
	q.Set("tag", aws.ToString(in.Tagging))
	u := "https://example.test/" + aws.ToString(in.Bucket) + "/" + aws.ToString(in.Key) + "?" + q.Encode()
	return &v4.PresignedHTTPRequest{URL: u, Method: "PUT"}, nil
}

func newTestService(ddb ddbAPI, ps presignAPI) *Service {
	s := New(Config{DDB: ddb, Presigner: ps, Table: "sessions", Bucket: "prod-bucket"})
	// Deterministic id/secret/clock for assertions.
	s.newID = func() string { return "sess_fixed" }
	s.newSecret = func() string { return "test-secret" }
	s.now = func() time.Time { return time.Unix(1_000_000, 0) }
	return s
}

func TestPresignUploadWritesSessionAndBindsTag(t *testing.T) {
	ddb := newFakeDDB()
	ps := &fakePresigner{}
	svc := newTestService(ddb, ps)

	resp, err := svc.PresignUpload(context.Background(), &runtimev1.PresignUploadRequest{
		Bucket:          "storage",
		CallbackBaseUrl: "https://app.example/api/blob",
		Metadata:        []byte(`{"user":"u1"}`),
		Files: []*runtimev1.PresignFile{
			{Key: "avatar.png", Name: "avatar.png", Size: 1024, MimeType: "image/png"},
		},
	})
	if err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}
	if resp.GetSessionId() != "sess_fixed" {
		t.Fatalf("session_id = %q", resp.GetSessionId())
	}
	if len(resp.GetFiles()) != 1 {
		t.Fatalf("targets = %d, want 1", len(resp.GetFiles()))
	}
	target := resp.GetFiles()[0]
	if !strings.Contains(target.GetUrl(), "prod-bucket/avatar.png") {
		t.Fatalf("url does not use the prod bucket + as-is key: %s", target.GetUrl())
	}

	// The presign input binds exact content-length, content-type, and the
	// session tag.
	in := ps.lastInput
	if aws.ToInt64(in.ContentLength) != 1024 {
		t.Fatalf("content-length not bound: %v", in.ContentLength)
	}
	if aws.ToString(in.ContentType) != "image/png" {
		t.Fatalf("content-type not bound: %v", in.ContentType)
	}
	if got := aws.ToString(in.Tagging); got != "sessionId=sess_fixed" {
		t.Fatalf("tag = %q, want sessionId=sess_fixed", got)
	}

	// The pending session landed in the store with the secret.
	sess, err := svc.store.get(context.Background(), "sess_fixed")
	if err != nil {
		t.Fatalf("get session: %v", err)
	}
	if sess.Secret != "test-secret" {
		t.Fatalf("secret not persisted: %q", sess.Secret)
	}
	if sess.CallbackBaseURL != "https://app.example/api/blob" {
		t.Fatalf("callback_base_url not persisted: %q", sess.CallbackBaseURL)
	}
	if len(sess.Files) != 1 || sess.Files[0].State != statePending {
		t.Fatalf("file not persisted pending: %+v", sess.Files)
	}
	if sess.ExpiresAt <= sess.CreatedAt {
		t.Fatalf("expires_at %d must be after created_at %d", sess.ExpiresAt, sess.CreatedAt)
	}
}

func TestVerifyUploadSignatureAcceptsAndRejects(t *testing.T) {
	ddb := newFakeDDB()
	svc := newTestService(ddb, &fakePresigner{})
	_, err := svc.PresignUpload(context.Background(), &runtimev1.PresignUploadRequest{
		Bucket:   "storage",
		Metadata: []byte("meta"),
		Files:    []*runtimev1.PresignFile{{Key: "k.png", Name: "k.png", Size: 10, MimeType: "image/png"}},
	})
	if err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}

	file := SignedFile{Key: "k.png", Name: "k.png", Size: 10, MimeType: "image/png"}
	sig := signUpload("test-secret", "sess_fixed", file)

	ok, err := svc.VerifyUploadSignature(context.Background(), &runtimev1.VerifyUploadSignatureRequest{
		SessionId: "sess_fixed",
		Signature: sig,
		File:      &runtimev1.CompletedFile{Key: "k.png", Name: "k.png", Size: 10, MimeType: "image/png"},
	})
	if err != nil {
		t.Fatalf("VerifyUploadSignature: %v", err)
	}
	if !ok.GetValid() || string(ok.GetMetadata()) != "meta" {
		t.Fatalf("valid signature should return metadata: %+v", ok)
	}

	bad, err := svc.VerifyUploadSignature(context.Background(), &runtimev1.VerifyUploadSignatureRequest{
		SessionId: "sess_fixed",
		Signature: "deadbeef",
		File:      &runtimev1.CompletedFile{Key: "k.png", Name: "k.png", Size: 10, MimeType: "image/png"},
	})
	if err != nil {
		t.Fatalf("VerifyUploadSignature(bad): %v", err)
	}
	if bad.GetValid() || bad.GetMetadata() != nil {
		t.Fatalf("forged signature must be rejected without metadata: %+v", bad)
	}

	missing, err := svc.VerifyUploadSignature(context.Background(), &runtimev1.VerifyUploadSignatureRequest{
		SessionId: "nope",
		Signature: sig,
		File:      &runtimev1.CompletedFile{Key: "k.png"},
	})
	if err != nil {
		t.Fatalf("VerifyUploadSignature(missing): %v", err)
	}
	if missing.GetValid() {
		t.Fatal("unknown session must not verify")
	}
}

func TestGetUploadStatusPendingAndExpired(t *testing.T) {
	ddb := newFakeDDB()
	svc := newTestService(ddb, &fakePresigner{})
	if _, err := svc.PresignUpload(context.Background(), &runtimev1.PresignUploadRequest{
		Bucket: "storage",
		Files:  []*runtimev1.PresignFile{{Key: "k", Name: "k", Size: 1, MimeType: "text/plain"}},
	}); err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}

	st, err := svc.GetUploadStatus(context.Background(), &runtimev1.GetUploadStatusRequest{SessionId: "sess_fixed"})
	if err != nil {
		t.Fatalf("GetUploadStatus: %v", err)
	}
	if st.GetState() != runtimev1.UploadState_UPLOAD_STATE_PENDING {
		t.Fatalf("state = %v, want PENDING", st.GetState())
	}

	// Advance the clock past the session TTL: status becomes terminally expired.
	svc.now = func() time.Time { return time.Unix(1_000_000, 0).Add(3 * time.Hour) }
	st, err = svc.GetUploadStatus(context.Background(), &runtimev1.GetUploadStatusRequest{SessionId: "sess_fixed"})
	if err != nil {
		t.Fatalf("GetUploadStatus: %v", err)
	}
	if st.GetState() != runtimev1.UploadState_UPLOAD_STATE_EXPIRED || st.GetError() == "" {
		t.Fatalf("expired session should report EXPIRED with error: %+v", st)
	}
}
