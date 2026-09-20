package bucket

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
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
	presignTTL = time.Hour
	sessionTTL = 2 * time.Hour

	reservedPrefix = constants.ProjectStateDirName + "/"
	sessionPrefix  = reservedPrefix + "sessions/"
)

// ObjectAPI is the slice of S3 a bucket's data plane reaches for.
type ObjectAPI interface {
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	GetObject(context.Context, *s3.GetObjectInput, ...func(*s3.Options)) (*s3.GetObjectOutput, error)
	PutObject(context.Context, *s3.PutObjectInput, ...func(*s3.Options)) (*s3.PutObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObjects(context.Context, *s3.DeleteObjectsInput, ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
	CopyObject(context.Context, *s3.CopyObjectInput, ...func(*s3.Options)) (*s3.CopyObjectOutput, error)
	CreateMultipartUpload(context.Context, *s3.CreateMultipartUploadInput, ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error)
	CompleteMultipartUpload(context.Context, *s3.CompleteMultipartUploadInput, ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error)
	AbortMultipartUpload(context.Context, *s3.AbortMultipartUploadInput, ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error)
}

// PresignAPI signs the requests a caller drives itself.
type PresignAPI interface {
	PresignGetObject(context.Context, *s3.GetObjectInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	PresignPutObject(context.Context, *s3.PutObjectInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	PresignUploadPart(context.Context, *s3.UploadPartInput, ...func(*s3.PresignOptions)) (*v4.PresignedHTTPRequest, error)
	PresignPostObject(context.Context, *s3.PutObjectInput, ...func(*s3.PresignPostOptions)) (*s3.PresignedPostRequest, error)
}

// Poster delivers a settled upload's callback to the app that asked for it.
type Poster interface {
	Post(ctx context.Context, url string, body []byte) error
}

// FreeSpace reports the bytes free and the bytes the store's volume holds.
type FreeSpace func() (free uint64, total uint64, err error)

// Config is what a vps box hands the bucket service it runs.
type Config struct {
	// Objects reaches the store over its internal address.
	Objects ObjectAPI
	// Internal signs urls the app itself drives, against the store's internal address.
	Internal PresignAPI
	// External signs urls a browser drives, against the store's public address, and
	// names that address. Both are empty while no domain points at the store.
	External func() (PresignAPI, string)
	// Callbacks delivers a settled upload to the app.
	Callbacks Poster
	// Volume reports the store volume's free space, nil where the store is not ours to watch.
	Volume FreeSpace
	// PostPolicies is true where the store signs POST policies, which bound an upload's size.
	PostPolicies bool
	// Sessions names the bucket, and optional key prefix, this project's upload sessions live under.
	Sessions string
}

// Service answers the bucket RPCs against any S3-compatible store.
type Service struct {
	cfg      Config
	sessions scope

	now       func() time.Time
	newID     func() string
	newSecret func() string
}

var _ bucketv1connect.BucketServiceHandler = (*Service)(nil)

// New builds the service a box's runtime proxy serves.
func New(cfg Config) *Service {
	return &Service{
		cfg:       cfg,
		sessions:  scopeOf(cfg.Sessions),
		now:       time.Now,
		newID:     func() string { return "sess_" + randomHex(16) },
		newSecret: func() string { return randomHex(32) },
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

func (s *Service) signer(audience bucketv1.SignedAudience) (PresignAPI, error) {
	if audience == bucketv1.SignedAudience_SIGNED_AUDIENCE_EXTERNAL {
		var signer PresignAPI
		var public string
		if s.cfg.External != nil {
			signer, public = s.cfg.External()
		}
		if signer == nil || public == "" {
			return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New(
				"this project has no domain bound, so its store has no address a browser could reach: bind a domain to this project, or keep the bytes behind your app"))
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
		return connect.NewError(connect.CodeResourceExhausted, fmt.Errorf(
			"the store volume has %d MiB free and this box keeps %d MiB in hand, so no write is signed until something is deleted or the box's disk grows",
			free>>20, floor>>20))
	}
	return nil
}

// PresignUpload opens a session and signs one target per file the app asked for.
func (s *Service) PresignUpload(ctx context.Context, req *bucketv1.PresignUploadRequest) (*bucketv1.PresignUploadResponse, error) {
	if err := s.roomToWrite(); err != nil {
		return nil, err
	}
	signer, err := s.signer(bucketv1.SignedAudience_SIGNED_AUDIENCE_EXTERNAL)
	if err != nil {
		return nil, err
	}

	held := scopeOf(req.GetBucket())
	sessionID := s.newID()
	now := s.now()

	files := make([]sessionFile, len(req.GetFiles()))
	targets := make([]*bucketv1.PresignedTarget, len(req.GetFiles()))
	for i, f := range req.GetFiles() {
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
		signed, err := signer.PresignPostObject(ctx, in, func(o *s3.PresignPostOptions) {
			o.Expires = presignTTL
			o.Conditions = conditions
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sign a browser upload of %q: %w", f.GetKey(), err))
		}
		return &bucketv1.PresignedTarget{
			Url:                signed.URL,
			Key:                f.GetKey(),
			Name:               f.GetName(),
			ContentDisposition: disposition,
			Method:             "POST",
			Fields:             signed.Values,
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

// VerifyUploadSignature says whether a callback carries this service's own signature.
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

// GetUploadStatus reads a session without touching the objects it covers.
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
		if lower := strings.ToLower(name); strings.HasPrefix(lower, "x-amz-") && len(values) > 0 {
			headers[lower] = values[0]
		}
	}
	return headers
}
