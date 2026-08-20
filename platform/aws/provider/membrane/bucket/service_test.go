package bucket

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"regexp"
	"strconv"
	"strings"
	"testing"
	"time"

	connect "connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	ddbtypes "github.com/aws/aws-sdk-go-v2/service/dynamodb/types"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	bucketsv1 "github.com/ocelhq/ocel/pkg/proto/buckets/v1"
)

type fakeDDB struct {
	items map[string]map[string]ddbtypes.AttributeValue
}

func newFakeDDB() *fakeDDB {
	return &fakeDDB{items: map[string]map[string]ddbtypes.AttributeValue{}}
}

func avString(v ddbtypes.AttributeValue) string {
	return v.(*ddbtypes.AttributeValueMemberS).Value
}

func itemKey(m map[string]ddbtypes.AttributeValue) string {
	return avString(m["pk"]) + "\x00" + avString(m["sk"])
}

func (f *fakeDDB) PutItem(_ context.Context, in *dynamodb.PutItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.PutItemOutput, error) {
	f.items[itemKey(in.Item)] = in.Item
	return &dynamodb.PutItemOutput{}, nil
}

func (f *fakeDDB) GetItem(_ context.Context, in *dynamodb.GetItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.GetItemOutput, error) {
	item, ok := f.items[itemKey(in.Key)]
	if !ok {
		return &dynamodb.GetItemOutput{}, nil
	}
	return &dynamodb.GetItemOutput{Item: item}, nil
}

var updateFileIdxRe = regexp.MustCompile(`files\[(\d+)\]`)

func (f *fakeDDB) UpdateItem(_ context.Context, in *dynamodb.UpdateItemInput, _ ...func(*dynamodb.Options)) (*dynamodb.UpdateItemOutput, error) {
	item, ok := f.items[itemKey(in.Key)]
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

const testSessionKeyPrefix = "PROJECT#shop#ENV#prod#SESSION#"

func newTestService(ddb ddbAPI, ps presignAPI) *Service {
	s := New(Config{DDB: ddb, Presigner: ps, Table: "sessions", SessionKeyPrefix: testSessionKeyPrefix})
	s.newID = func() string { return "sess_fixed" }
	s.newSecret = func() string { return "test-secret" }
	s.now = func() time.Time { return time.Unix(1_000_000, 0) }
	return s
}

func TestPresignUpload(t *testing.T) {
	t.Parallel()

	t.Run("writes the session and binds the object to it by tag", func(t *testing.T) {
		t.Parallel()
		ddb := newFakeDDB()
		ps := &fakePresigner{}
		svc := newTestService(ddb, ps)

		resp, err := svc.PresignUpload(context.Background(), &bucketsv1.PresignUploadRequest{
			Bucket:          "storage",
			CallbackBaseUrl: "https://app.example/api/blob",
			Metadata:        []byte(`{"user":"u1"}`),
			Files: []*bucketsv1.PresignFile{
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
		if !strings.Contains(target.GetUrl(), "storage/avatar.png") {
			t.Fatalf("url does not use the requested bucket + as-is key: %s", target.GetUrl())
		}
		if sess, err := svc.store.get(context.Background(), "sess_fixed"); err != nil || sess.Bucket != "storage" {
			t.Fatalf("session bucket = %q (err %v), want the bucket the request named", sess.Bucket, err)
		}

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
		if sess.PK != testSessionKeyPrefix+"sess_fixed" {
			t.Fatalf("pk = %q, want %q — every other deploy sharing this account's table reads and writes under its own scope",
				sess.PK, testSessionKeyPrefix+"sess_fixed")
		}
	})
}

func TestVerifyUploadSignature(t *testing.T) {
	t.Parallel()

	const key = "k.png"
	file := SignedFile{Key: key, Name: key, Size: 10, MimeType: "image/png"}
	completed := func() *bucketsv1.CompletedFile {
		return &bucketsv1.CompletedFile{Key: key, Name: key, Size: 10, MimeType: "image/png"}
	}
	seeded := func(t *testing.T) *Service {
		t.Helper()
		svc := newTestService(newFakeDDB(), &fakePresigner{})
		if _, err := svc.PresignUpload(context.Background(), &bucketsv1.PresignUploadRequest{
			Bucket:   "storage",
			Metadata: []byte("meta"),
			Files:    []*bucketsv1.PresignFile{{Key: key, Name: key, Size: 10, MimeType: "image/png"}},
		}); err != nil {
			t.Fatalf("PresignUpload: %v", err)
		}
		return svc
	}

	t.Run("a genuine signature returns the session metadata", func(t *testing.T) {
		t.Parallel()
		svc := seeded(t)

		got, err := svc.VerifyUploadSignature(context.Background(), &bucketsv1.VerifyUploadSignatureRequest{
			SessionId: "sess_fixed",
			Signature: mustSign(t, "test-secret", "sess_fixed", file),
			File:      completed(),
		})
		if err != nil {
			t.Fatalf("VerifyUploadSignature: %v", err)
		}
		if !got.GetValid() || string(got.GetMetadata()) != "meta" {
			t.Fatalf("valid signature should return metadata: %+v", got)
		}
	})

	t.Run("a forged signature is rejected without metadata", func(t *testing.T) {
		t.Parallel()
		svc := seeded(t)

		got, err := svc.VerifyUploadSignature(context.Background(), &bucketsv1.VerifyUploadSignatureRequest{
			SessionId: "sess_fixed",
			Signature: "deadbeef",
			File:      completed(),
		})
		if err != nil {
			t.Fatalf("VerifyUploadSignature: %v", err)
		}
		if got.GetValid() || got.GetMetadata() != nil {
			t.Fatalf("forged signature must be rejected without metadata: %+v", got)
		}
	})

	t.Run("an unknown session does not verify", func(t *testing.T) {
		t.Parallel()
		svc := seeded(t)

		got, err := svc.VerifyUploadSignature(context.Background(), &bucketsv1.VerifyUploadSignatureRequest{
			SessionId: "nope",
			Signature: mustSign(t, "test-secret", "sess_fixed", file),
			File:      &bucketsv1.CompletedFile{Key: key},
		})
		if err != nil {
			t.Fatalf("VerifyUploadSignature: %v", err)
		}
		if got.GetValid() {
			t.Fatal("unknown session must not verify")
		}
	})
}

func TestGetUploadStatus(t *testing.T) {
	t.Parallel()

	svc := newTestService(newFakeDDB(), &fakePresigner{})
	if _, err := svc.PresignUpload(context.Background(), &bucketsv1.PresignUploadRequest{
		Bucket: "storage",
		Files:  []*bucketsv1.PresignFile{{Key: "k", Name: "k", Size: 1, MimeType: "text/plain"}},
	}); err != nil {
		t.Fatalf("PresignUpload: %v", err)
	}

	t.Run("an upload nobody has finished is pending", func(t *testing.T) {
		st, err := svc.GetUploadStatus(context.Background(), &bucketsv1.GetUploadStatusRequest{SessionId: "sess_fixed"})
		if err != nil {
			t.Fatalf("GetUploadStatus: %v", err)
		}
		if st.GetState() != bucketsv1.UploadState_UPLOAD_STATE_PENDING {
			t.Fatalf("state = %v, want PENDING", st.GetState())
		}
	})

	t.Run("a session read after its ttl is expired, with a reason", func(t *testing.T) {
		svc.now = func() time.Time { return time.Unix(1_000_000, 0).Add(3 * time.Hour) }

		st, err := svc.GetUploadStatus(context.Background(), &bucketsv1.GetUploadStatusRequest{SessionId: "sess_fixed"})
		if err != nil {
			t.Fatalf("GetUploadStatus: %v", err)
		}
		if st.GetState() != bucketsv1.UploadState_UPLOAD_STATE_EXPIRED || st.GetError() == "" {
			t.Fatalf("expired session should report EXPIRED with error: %+v", st)
		}
	})

	t.Run("an unknown session is not found", func(t *testing.T) {
		_, err := svc.GetUploadStatus(context.Background(), &bucketsv1.GetUploadStatusRequest{SessionId: "nope"})

		var connectErr *connect.Error
		if !errors.As(err, &connectErr) || connectErr.Code() != connect.CodeNotFound {
			t.Fatalf("GetUploadStatus on an unknown session err = %v, want CodeNotFound", err)
		}
	})
}
