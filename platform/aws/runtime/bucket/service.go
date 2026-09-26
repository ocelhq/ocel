package bucket

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	connect "connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/constants"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
)

const (
	presignTTL    = time.Hour
	maxPresignTTL = 7 * 24 * time.Hour
	sessionTTL    = 2 * time.Hour

	sessionTagKey = "sessionId"
)

type presignAPI interface {
	PresignPutObject(context.Context, *s3.PutObjectInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	PresignGetObject(context.Context, *s3.GetObjectInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	PresignUploadPart(context.Context, *s3.UploadPartInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	PresignPostObject(context.Context, *s3.PutObjectInput, ...func(*s3.PresignPostOptions)) (*s3.PresignedPostRequest, error)
}

type Service struct {
	store     *sessionStore
	presigner presignAPI
	objects   objectAPI
	granted   func() []string

	now       func() time.Time
	newID     func() string
	newSecret func() string
}

var _ bucketv1connect.BucketServiceHandler = (*Service)(nil)

type Config struct {
	DDB              ddbAPI
	Presigner        presignAPI
	Objects          objectAPI
	Table            string
	SessionKeyPrefix string
	Granted          func() []string
}

func New(cfg Config) *Service {
	granted := cfg.Granted
	if granted == nil {
		granted = func() []string { return nil }
	}
	return &Service{
		store:     &sessionStore{client: cfg.DDB, table: cfg.Table, keyPrefix: cfg.SessionKeyPrefix},
		presigner: cfg.Presigner,
		objects:   cfg.Objects,
		granted:   granted,
		now:       time.Now,
		newID:     func() string { return "sess_" + randomHex(16) },
		newSecret: func() string { return randomHex(32) },
	}
}

func (s *Service) checkGranted(bucket string) error {
	if slices.Contains(s.granted(), bucket) {
		return nil
	}
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf(
		"this app was granted no bucket called %q, and a deployment reaches the buckets its own code declares and the ones it is granted and nothing else", bucket))
}

func (s *Service) reach(bucket string, keys ...string) error {
	if err := s.checkGranted(bucket); err != nil {
		return err
	}
	for _, key := range keys {
		if !strings.HasPrefix(key, constants.ReservedKeyPrefix) {
			continue
		}
		return connect.NewError(connect.CodePermissionDenied, fmt.Errorf(
			"%q names a key under %s, which is where the store keeps its own bookkeeping and is no app's to read or write",
			key, constants.ReservedKeyPrefix))
	}
	return nil
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

func (s *Service) PresignUpload(ctx context.Context, req *bucketv1.PresignUploadRequest) (*bucketv1.PresignUploadResponse, error) {
	keys := make([]string, 0, len(req.GetFiles()))
	for _, f := range req.GetFiles() {
		keys = append(keys, f.GetKey())
	}
	if err := s.reach(req.GetBucket(), keys...); err != nil {
		return nil, err
	}
	sessionID := s.newID()
	secret := s.newSecret()
	now := s.now()

	files := make([]sessionFile, len(req.GetFiles()))
	targets := make([]*bucketv1.PresignedTarget, len(req.GetFiles()))

	for i, f := range req.GetFiles() {
		url, headers, err := s.presignPut(ctx, req.GetBucket(), f.GetKey(), f.GetMimeType(), f.GetSize(), sessionID, req.GetContentDisposition())
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("presign %q: %w", f.GetKey(), err))
		}
		files[i] = sessionFile{
			Key:      f.GetKey(),
			Name:     f.GetName(),
			Size:     f.GetSize(),
			MimeType: f.GetMimeType(),
			State:    statePending,
		}
		targets[i] = &bucketv1.PresignedTarget{
			Url:                url,
			Key:                f.GetKey(),
			Name:               f.GetName(),
			ContentDisposition: req.GetContentDisposition(),
			Headers:            headers,
		}
	}

	sess := session{
		SessionID:          sessionID,
		Secret:             secret,
		Bucket:             req.GetBucket(),
		CallbackBaseURL:    req.GetCallbackBaseUrl(),
		ContentDisposition: req.GetContentDisposition(),
		Metadata:           req.GetMetadata(),
		Files:              files,
		CreatedAt:          now.Unix(),
		ExpiresAt:          now.Add(sessionTTL).Unix(),
	}
	if err := s.store.put(ctx, sess); err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	return &bucketv1.PresignUploadResponse{SessionId: sessionID, Files: targets}, nil
}

const vendorHeaderPrefix = "x-amz-"

func (s *Service) presignPut(ctx context.Context, bucket, key, contentType string, size int64, sessionID, contentDisposition string) (string, map[string]string, error) {
	in := &s3.PutObjectInput{
		Bucket:        aws.String(bucket),
		Key:           aws.String(key),
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(size),
		Tagging:       aws.String(sessionTagKey + "=" + sessionID),
	}
	if contentDisposition != "" {
		in.ContentDisposition = aws.String(contentDisposition)
	}
	req, err := s.presigner.PresignPutObject(ctx, in, func(o *s3.PresignOptions) {
		o.Expires = presignTTL
	})
	if err != nil {
		return "", nil, err
	}
	headers := map[string]string{}
	for name, values := range req.SignedHeader {
		if lower := strings.ToLower(name); strings.HasPrefix(lower, vendorHeaderPrefix) && len(values) > 0 {
			headers[lower] = values[0]
		}
	}
	return req.URL, headers, nil
}

func (s *Service) VerifyUploadSignature(ctx context.Context, req *bucketv1.VerifyUploadSignatureRequest) (*bucketv1.VerifyUploadSignatureResponse, error) {
	sess, err := s.store.get(ctx, req.GetSessionId())
	if errors.Is(err, errSessionNotFound) {
		return &bucketv1.VerifyUploadSignatureResponse{Valid: false}, nil
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	f := req.GetFile()
	file := SignedFile{
		Key:      f.GetKey(),
		Name:     f.GetName(),
		Size:     f.GetSize(),
		MimeType: f.GetMimeType(),
	}
	valid, err := verifyUpload(sess.Secret, req.GetSessionId(), file, req.GetSignature())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !valid {
		return &bucketv1.VerifyUploadSignatureResponse{Valid: false}, nil
	}
	return &bucketv1.VerifyUploadSignatureResponse{Valid: true, Metadata: sess.Metadata}, nil
}

func (s *Service) GetUploadStatus(ctx context.Context, req *bucketv1.GetUploadStatusRequest) (*bucketv1.GetUploadStatusResponse, error) {
	sess, err := s.store.get(ctx, req.GetSessionId())
	if errors.Is(err, errSessionNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	state := aggregateState(sess.Files)
	if s.now().Unix() >= sess.ExpiresAt {
		state = stateExpired
	}

	resp := &bucketv1.GetUploadStatusResponse{State: toProtoState(state)}
	if state == stateExpired {
		resp.Error = "upload expired"
	}
	return resp, nil
}

func toProtoState(s fileState) bucketv1.UploadState {
	switch s {
	case statePending:
		return bucketv1.UploadState_UPLOAD_STATE_PENDING
	case stateSucceeded:
		return bucketv1.UploadState_UPLOAD_STATE_SUCCEEDED
	case stateExpired:
		return bucketv1.UploadState_UPLOAD_STATE_EXPIRED
	default:
		return bucketv1.UploadState_UPLOAD_STATE_UNSPECIFIED
	}
}
