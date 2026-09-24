package bucket

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"strings"
	"sync"
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

	sessionPrefix = constants.ReservedKeyPrefix + "sessions/"
)

type ObjectAPI interface {
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObjects(context.Context, *s3.DeleteObjectsInput, ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
	CopyObject(context.Context, *s3.CopyObjectInput, ...func(*s3.Options)) (*s3.CopyObjectOutput, error)
	CreateMultipartUpload(context.Context, *s3.CreateMultipartUploadInput, ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error)
	ListMultipartUploads(context.Context, *s3.ListMultipartUploadsInput, ...func(*s3.Options)) (*s3.ListMultipartUploadsOutput, error)
	CompleteMultipartUpload(context.Context, *s3.CompleteMultipartUploadInput, ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error)
	AbortMultipartUpload(context.Context, *s3.AbortMultipartUploadInput, ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error)
}

type PresignAPI interface {
	PresignGetObject(context.Context, *s3.GetObjectInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	PresignPutObject(context.Context, *s3.PutObjectInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	PresignUploadPart(context.Context, *s3.UploadPartInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	PresignPostObject(context.Context, *s3.PutObjectInput, ...func(*s3.PresignPostOptions)) (*s3.PresignedPostRequest, error)
}

type Poster interface {
	Post(ctx context.Context, url string, body []byte) error
}

type FreeSpace func() (free uint64, total uint64, err error)

type Config struct {
	Objects      ObjectAPI
	Internal     PresignAPI
	External     func(context.Context) (PresignAPI, string)
	Callbacks    Poster
	Volume       FreeSpace
	PostPolicies bool
	SweepUploads bool
	Sessions     string
	Granted      []string
}

type Service struct {
	cfg      Config
	sessions scope
	granted  map[string]scope

	now       func() time.Time
	newID     func() string
	newSecret func() string
	sweeping  func(run func())

	mu    sync.Mutex
	swept map[string]time.Time
}

var _ bucketv1connect.BucketServiceHandler = (*Service)(nil)

func New(cfg Config) *Service {
	granted := make(map[string]scope, len(cfg.Granted))
	for _, name := range cfg.Granted {
		granted[name] = scopeOf(name)
	}
	return &Service{
		cfg:       cfg,
		sessions:  scopeOf(cfg.Sessions),
		granted:   granted,
		now:       time.Now,
		newID:     func() string { return "sess_" + randomHex(16) },
		newSecret: func() string { return randomHex(32) },
		sweeping:  func(run func()) { go run() },
	}
}

func randomHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		panic(err)
	}
	return hex.EncodeToString(b)
}

type scope struct {
	bucket string
	prefix string
}

func (s scope) key(key string) string {
	return s.prefix + key
}

func (s scope) strip(key string) string {
	return strings.TrimPrefix(key, s.prefix)
}

func scopeOf(spec string) scope {
	bucket, prefix, found := strings.Cut(spec, "/")
	if !found || prefix == "" {
		return scope{bucket: spec}
	}
	return scope{bucket: bucket, prefix: strings.TrimSuffix(prefix, "/") + "/"}
}

func (s *Service) held(name string) (scope, error) {
	if granted, ok := s.granted[name]; ok {
		return granted, nil
	}
	return scope{}, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("this app was granted no bucket called %q", name))
}

func (s *Service) reach(name, key string) (scope, string, error) {
	granted, err := s.held(name)
	if err != nil {
		return scope{}, "", err
	}
	if err := reserved(key); err != nil {
		return scope{}, "", err
	}
	return granted, granted.key(key), nil
}

func reserved(key string) error {
	if !strings.HasPrefix(key, constants.ReservedKeyPrefix) {
		return nil
	}
	return connect.NewError(connect.CodePermissionDenied, fmt.Errorf("%q is under the reserved prefix %s", key, constants.ReservedKeyPrefix))
}

func (s *Service) signer(ctx context.Context, audience bucketv1.SignedAudience) (PresignAPI, error) {
	if audience == bucketv1.SignedAudience_SIGNED_AUDIENCE_EXTERNAL {
		var signer PresignAPI
		var public string
		if s.cfg.External != nil {
			signer, public = s.cfg.External(ctx)
		}
		if signer == nil || public == "" {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(
				"this project has no domain bound, so its store has no public address\nBind a domain to this project"))
		}
		return signer, nil
	}
	return s.cfg.Internal, nil
}

const minimumFreeBytes = 1 << 30

func (s *Service) roomToWrite() error {
	if s.cfg.Volume == nil {
		return nil
	}
	free, total, err := s.cfg.Volume()
	if err != nil {
		return connect.NewError(connect.CodeInternal, fmt.Errorf("read the store volume's free space: %w", err))
	}
	floor := uint64(minimumFreeBytes)
	if tenth := total / 10; tenth > floor {
		floor = tenth
	}
	if free < floor {
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf("the store volume has %d MiB free, below the %d MiB floor", free>>20, floor>>20))
	}
	return nil
}

func (s *Service) PresignUpload(ctx context.Context, req *bucketv1.PresignUploadRequest) (*bucketv1.PresignUploadResponse, error) {
	if err := s.roomToWrite(); err != nil {
		return nil, err
	}
	signer, err := s.signer(ctx, bucketv1.SignedAudience_SIGNED_AUDIENCE_EXTERNAL)
	if err != nil {
		return nil, err
	}

	held, err := s.held(req.GetBucket())
	if err != nil {
		return nil, err
	}
	sessionID := s.newID()
	now := s.now()

	files := make([]sessionFile, len(req.GetFiles()))
	targets := make([]*bucketv1.PresignedTarget, len(req.GetFiles()))
	for i, f := range req.GetFiles() {
		if err := reserved(f.GetKey()); err != nil {
			return nil, err
		}
		target, err := s.signUpload(ctx, signer, held, f, req.GetContentDisposition())
		if err != nil {
			return nil, err
		}
		files[i] = sessionFile{
			Key:      f.GetKey(),
			Name:     f.GetName(),
			Size:     f.GetSize(),
			MimeType: f.GetMimeType(),
			State:    statePending,
		}
		targets[i] = target
	}

	sess := session{
		SessionID:          sessionID,
		Secret:             s.newSecret(),
		Bucket:             req.GetBucket(),
		CallbackBaseURL:    req.GetCallbackBaseUrl(),
		ContentDisposition: req.GetContentDisposition(),
		Metadata:           req.GetMetadata(),
		Files:              files,
		CreatedAt:          now.Unix(),
		ExpiresAt:          now.Add(sessionTTL).Unix(),
	}
	if err := s.createSession(ctx, sess); err != nil {
		return nil, err
	}
	return &bucketv1.PresignUploadResponse{SessionId: sessionID, Files: targets}, nil
}

func (s *Service) signUpload(ctx context.Context, signer PresignAPI, held scope, f *bucketv1.PresignFile, disposition string) (*bucketv1.PresignedTarget, error) {
	in := &s3.PutObjectInput{
		Bucket:      aws.String(held.bucket),
		Key:         aws.String(held.key(f.GetKey())),
		ContentType: aws.String(f.GetMimeType()),
	}
	if disposition != "" {
		in.ContentDisposition = aws.String(disposition)
	}

	if s.cfg.PostPolicies {
		conditions := []any{
			map[string]string{"Content-Type": f.GetMimeType()},
			[]any{"content-length-range", f.GetSize(), f.GetSize()},
		}
		if disposition != "" {
			conditions = append(conditions, map[string]string{"Content-Disposition": disposition})
		}
		signed, err := signer.PresignPostObject(ctx, in, func(o *s3.PresignPostOptions) {
			o.Expires = presignTTL
			o.Conditions = conditions
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sign a browser upload of %q: %w", f.GetKey(), err))
		}
		form := map[string]string{"Content-Type": f.GetMimeType()}
		maps.Copy(form, signed.Values)
		return &bucketv1.PresignedTarget{
			Url:                signed.URL,
			Key:                f.GetKey(),
			Name:               f.GetName(),
			ContentDisposition: disposition,
			Method:             "POST",
			Fields:             form,
		}, nil
	}

	in.ContentLength = aws.Int64(f.GetSize())
	signed, err := signer.PresignPutObject(ctx, in, func(o *s3.PresignOptions) { o.Expires = presignTTL })
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sign an upload of %q: %w", f.GetKey(), err))
	}
	return &bucketv1.PresignedTarget{
		Url:                signed.URL,
		Key:                f.GetKey(),
		Name:               f.GetName(),
		ContentDisposition: disposition,
		Method:             signed.Method,
		Headers:            vendorHeaders(signed.SignedHeader),
	}, nil
}

func (s *Service) VerifyUploadSignature(ctx context.Context, req *bucketv1.VerifyUploadSignatureRequest) (*bucketv1.VerifyUploadSignatureResponse, error) {
	sess, err := s.anySession(ctx, req.GetSessionId())
	if errors.Is(err, errSessionNotFound) {
		return &bucketv1.VerifyUploadSignatureResponse{Valid: false}, nil
	}
	if err != nil {
		return nil, err
	}
	f := req.GetFile()
	valid, err := verifyUpload(sess.Secret, req.GetSessionId(), signedFile{
		Key:      f.GetKey(),
		Name:     f.GetName(),
		Size:     f.GetSize(),
		MimeType: f.GetMimeType(),
	}, req.GetSignature())
	if err != nil {
		return nil, connect.NewError(connect.CodeInternal, err)
	}
	if !valid {
		return &bucketv1.VerifyUploadSignatureResponse{Valid: false}, nil
	}
	return &bucketv1.VerifyUploadSignatureResponse{Valid: true, Metadata: sess.Metadata}, nil
}

func (s *Service) GetUploadStatus(ctx context.Context, req *bucketv1.GetUploadStatusRequest) (*bucketv1.GetUploadStatusResponse, error) {
	sess, err := s.anySession(ctx, req.GetSessionId())
	if errors.Is(err, errSessionNotFound) {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	if err != nil {
		return nil, err
	}
	state, reason := s.settled(sess)
	return &bucketv1.GetUploadStatusResponse{State: toProtoState(state), Error: reason}, nil
}

func (s *Service) settled(sess session) (fileState, string) {
	if s.now().Unix() >= sess.ExpiresAt && aggregate(sess.Files) != stateSucceeded {
		return stateExpired, "upload expired"
	}
	return aggregate(sess.Files), sess.Error
}

func toProtoState(state fileState) bucketv1.UploadState {
	switch state {
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

func vendorHeaders(signed map[string][]string) map[string]string {
	headers := map[string]string{}
	for name, values := range signed {
		lower := strings.ToLower(name)
		if len(values) == 0 || lower == "host" || lower == "content-length" {
			continue
		}
		headers[lower] = values[0]
	}
	return headers
}

const metadataCap = 2048

func withinMetadataCap(metadata map[string]string) error {
	held := 0
	for name, value := range metadata {
		held += len(name) + len(value)
	}
	if held <= metadataCap {
		return nil
	}
	return connect.NewError(connect.CodeInvalidArgument, fmt.Errorf("object metadata is %d bytes, over the %d-byte limit", held, metadataCap))
}
