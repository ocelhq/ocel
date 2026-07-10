package runtime

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"time"

	connect "connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	runtimev1 "github.com/ocelhq/ocel/pkg/proto/runtime/v1"
	"github.com/ocelhq/ocel/pkg/proto/runtime/v1/runtimev1connect"
)

// presignTTL is how long a minted PUT URL is valid; sessionTTL is strictly
// greater so the expiry window never reaps a still-live URL.
const (
	presignTTL = time.Hour
	sessionTTL = 2 * time.Hour

	// sessionTagKey is the object-tag key the presigned PUT binds. The prod
	// listener (T8) reads this tag off the landed object to resolve its session
	// — collision-safe, unlike keying by the object key.
	sessionTagKey = "sessionId"
)

// presignAPI is the subset of the S3 presign client the service uses, narrowed
// for testability.
type presignAPI interface {
	PresignPutObject(context.Context, *s3.PutObjectInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
}

// Service is the production RuntimeService: it mints presigned PUT targets,
// persists pending sessions to DynamoDB, verifies completion signatures, and
// reports status. It implements runtimev1connect.RuntimeServiceHandler.
type Service struct {
	store     *sessionStore
	presigner presignAPI
	bucket    string

	// now and generators are injectable so tests are deterministic; production
	// wires the wall clock and crypto/rand.
	now       func() time.Time
	newID     func() string
	newSecret func() string
}

var _ runtimev1connect.RuntimeServiceHandler = (*Service)(nil)

// Config wires a Service to its concrete AWS clients.
type Config struct {
	DDB       ddbAPI
	Presigner presignAPI
	Table     string
	Bucket    string
}

// New builds a Service from concrete clients, defaulting the clock and
// id/secret generators to production implementations.
func New(cfg Config) *Service {
	return &Service{
		store:     &sessionStore{client: cfg.DDB, table: cfg.Table},
		presigner: cfg.Presigner,
		bucket:    cfg.Bucket,
		now:       time.Now,
		newID:     func() string { return "sess_" + randomHex(16) },
		newSecret: func() string { return randomHex(32) },
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand.Read never returns an error on supported platforms; a
		// failure here is unrecoverable.
		panic(fmt.Sprintf("runtime: read random: %v", err))
	}
	return hex.EncodeToString(b)
}

func (s *Service) PresignUpload(ctx context.Context, req *runtimev1.PresignUploadRequest) (*runtimev1.PresignUploadResponse, error) {
	if len(req.GetFiles()) == 0 {
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("presign requires at least one file"))
	}

	sessionID := s.newID()
	secret := s.newSecret()
	now := s.now()

	files := make([]sessionFile, len(req.GetFiles()))
	targets := make([]*runtimev1.PresignedTarget, len(req.GetFiles()))

	for i, f := range req.GetFiles() {
		url, err := s.presignPut(ctx, f.GetKey(), f.GetMimeType(), f.GetSize(), sessionID)
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
		targets[i] = &runtimev1.PresignedTarget{
			Url:  url,
			Key:  f.GetKey(),
			Name: f.GetName(),
		}
	}

	sess := session{
		SessionID:          sessionID,
		Secret:             secret,
		Bucket:             s.bucket,
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

	return &runtimev1.PresignUploadResponse{SessionId: sessionID, Files: targets}, nil
}

// presignPut mints a presigned PUT that binds Content-Length (exact) and
// Content-Type as signed conditions and a SigV4-signed x-amz-tagging carrying
// the session id, so a client cannot exceed declared limits or alter the tag
// without breaking the signature. The user's key is used as-is: prod has no
// tenancy prefix (the env owns its bucket).
func (s *Service) presignPut(ctx context.Context, key, contentType string, size int64, sessionID string) (string, error) {
	req, err := s.presigner.PresignPutObject(ctx, &s3.PutObjectInput{
		Bucket:        aws.String(s.bucket),
		Key:           aws.String(key),
		ContentType:   aws.String(contentType),
		ContentLength: aws.Int64(size),
		Tagging:       aws.String(sessionTagKey + "=" + sessionID),
	}, func(o *s3.PresignOptions) {
		o.Expires = presignTTL
	})
	if err != nil {
		return "", err
	}
	return req.URL, nil
}

func (s *Service) VerifyUploadSignature(ctx context.Context, req *runtimev1.VerifyUploadSignatureRequest) (*runtimev1.VerifyUploadSignatureResponse, error) {
	sess, err := s.store.get(ctx, req.GetSessionId())
	if errors.Is(err, errSessionNotFound) {
		return &runtimev1.VerifyUploadSignatureResponse{Valid: false}, nil
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
	if !verifyUpload(sess.Secret, req.GetSessionId(), file, req.GetSignature()) {
		return &runtimev1.VerifyUploadSignatureResponse{Valid: false}, nil
	}
	return &runtimev1.VerifyUploadSignatureResponse{Valid: true, Metadata: sess.Metadata}, nil
}

func (s *Service) GetUploadStatus(ctx context.Context, req *runtimev1.GetUploadStatusRequest) (*runtimev1.GetUploadStatusResponse, error) {
	sess, err := s.store.get(ctx, req.GetSessionId())
	if errors.Is(err, errSessionNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}

	// The DDB TTL reaps orphaned items lazily; treat a session past its TTL as
	// expired at read time so poll reaches a terminal state without waiting on
	// physical deletion.
	state := aggregateState(sess.Files)
	if s.now().Unix() >= sess.ExpiresAt {
		state = stateExpired
	}

	resp := &runtimev1.GetUploadStatusResponse{State: toProtoState(state)}
	if state == stateExpired {
		resp.Error = "upload expired"
	}
	return resp, nil
}

func toProtoState(s fileState) runtimev1.UploadState {
	switch s {
	case statePending:
		return runtimev1.UploadState_UPLOAD_STATE_PENDING
	case stateSucceeded:
		return runtimev1.UploadState_UPLOAD_STATE_SUCCEEDED
	case stateExpired:
		return runtimev1.UploadState_UPLOAD_STATE_EXPIRED
	default:
		return runtimev1.UploadState_UPLOAD_STATE_UNSPECIFIED
	}
}
