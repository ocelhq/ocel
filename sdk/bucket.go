package ocel

import (
	"context"
	"errors"
	"fmt"
	"iter"
	"net/http"
	"net/url"
	"os"
	"runtime"
	"sync"
	"time"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/durationpb"
	bucketv1 "ocel.dev/internal/proto/app/bucket/v1"
	"ocel.dev/internal/proto/app/bucket/v1/bucketv1connect"
	resourcesv1 "ocel.dev/internal/proto/app/resources/v1"
	bindingsv1 "ocel.dev/internal/proto/common/bindings/v1"
)

const (
	singleRequestCeiling = 16 << 20
	defaultPartSize      = 8 << 20
	partsInFlight        = 4
)

// ErrObjectNotFound is what every operation that cannot answer with nothing
// reports when the bucket has no object under the key. Match it with
// [errors.Is].
var ErrObjectNotFound = errors.New("the bucket holds no object under this key")

// ErrPreconditionFailed is what a write conditioned with [IfNotExists] or
// [IfMatch] reports when the object did not meet the condition. Match it with
// [errors.Is].
var ErrPreconditionFailed = errors.New("the object did not meet the condition this write carried")

// An Object is what a bucket knows about one object it stores.
type Object struct {
	// Key is the key the object is addressed by.
	Key string
	// Size is the object's length in bytes.
	Size int64
	// ETag is the store's opaque version tag for these bytes.
	ETag string
	// ContentType is the media type the object was written with.
	ContentType string
	// UploadedAt is when the object last took its current bytes, as the store
	// reports it.
	UploadedAt time.Time
	// Metadata is the user metadata written alongside the object.
	Metadata map[string]string
}

// A SignedUpload is a target someone without a credential writes one object
// through, for as long as it stays valid.
type SignedUpload struct {
	// URL is where the body is sent.
	URL string
	// Method is the HTTP method to send it with.
	Method string
	// Headers are the headers the signature covers, which the caller must send
	// unchanged.
	Headers map[string]string
	// Fields are the form fields the caller must send to a POST target, and are
	// empty for a PUT target.
	Fields map[string]string
	// Expires is when the target stops being valid, and is the zero time when
	// the lifetime was left to the runtime.
	Expires time.Time
}

// A BucketOption tunes the bucket [Bucket] declares.
type BucketOption func(*resourcesv1.BucketConfig)

// BucketPublic serves every object in the bucket anonymously over HTTP.
func BucketPublic() BucketOption {
	return func(c *resourcesv1.BucketConfig) { c.Public = true }
}

// BucketAllowedOrigins names the browser origins allowed to upload straight to
// the store.
func BucketAllowedOrigins(origins ...string) BucketOption {
	return func(c *resourcesv1.BucketConfig) { c.AllowedOrigins = origins }
}

// A BucketStore is a bucket an app declares and reads and writes its objects
// through.
type BucketStore struct {
	name string

	http          *http.Client
	singleCeiling int64
	partSize      int64

	once    sync.Once
	reached *reachedBucket
	err     error
}

type reachedBucket struct {
	client        bucketv1connect.BucketServiceClient
	bucket        string
	publicBaseURL string
}

// Bucket declares a bucket named name and returns the handle an app reads and
// writes its objects through. Call it from a file under the project's discovery
// folder: during discovery the call is the declaration, and at runtime it reads
// the binding the deploy delivered for that name.
func Bucket(name string, opts ...BucketOption) *BucketStore {
	if discovering() {
		config := &resourcesv1.BucketConfig{}
		for _, opt := range opts {
			opt(config)
		}
		_, file, line, _ := runtime.Caller(1)
		err := declare(&resourcesv1.DeclareRequest{
			Resource: &resourcesv1.ResourceIdentifier{
				Type: resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET,
				Name: name,
			},
			Config: &resourcesv1.DeclareRequest_Bucket{Bucket: config},
			Source: fmt.Sprintf("%s:%d", file, line),
		})
		if err != nil {
			panic(fmt.Sprintf("ocel: declare bucket %q: %v", name, err))
		}
	}
	return &BucketStore{
		name:          name,
		http:          http.DefaultClient,
		singleCeiling: singleRequestCeiling,
		partSize:      defaultPartSize,
	}
}

// Name is the name the bucket was declared under.
func (b *BucketStore) Name() string { return b.name }

// Attrs is what the bucket knows about the object under key. It reports
// [ErrObjectNotFound] when the bucket has none.
func (b *BucketStore) Attrs(ctx context.Context, key string) (*Object, error) {
	obj, err := b.head(ctx, "Attrs", key)
	if err != nil {
		return nil, err
	}
	if obj == nil {
		return nil, objectNotFound(key)
	}
	return obj, nil
}

// Exists reports whether the bucket has an object under key.
func (b *BucketStore) Exists(ctx context.Context, key string) (bool, error) {
	obj, err := b.head(ctx, "Exists", key)
	if err != nil {
		return false, err
	}
	return obj != nil, nil
}

// Delete removes the objects under keys. A key with no object under it is not an
// error.
func (b *BucketStore) Delete(ctx context.Context, keys ...string) error {
	if len(keys) == 0 {
		return nil
	}
	reached, err := b.runtime("Delete")
	if err != nil {
		return err
	}
	_, err = reached.client.Delete(ctx, &bucketv1.DeleteRequest{
		Bucket: reached.bucket,
		Keys:   keys,
	})
	return err
}

// Copy copies the object under src to dst within the same bucket. It reports
// [ErrObjectNotFound] when the bucket has nothing under src.
func (b *BucketStore) Copy(ctx context.Context, dst, src string) (*Object, error) {
	reached, err := b.runtime("Copy")
	if err != nil {
		return nil, err
	}
	res, err := reached.client.Copy(ctx, &bucketv1.CopyRequest{
		Bucket:         reached.bucket,
		SourceKey:      src,
		DestinationKey: dst,
	})
	if err != nil {
		return nil, refused(src, err)
	}
	return objectFrom(res.GetObject()), nil
}

// List walks the objects in the bucket, a page at a time, and yields the
// first error it meets before stopping.
func (b *BucketStore) List(ctx context.Context, opts ...ListOption) iter.Seq2[*Object, error] {
	return func(yield func(*Object, error) bool) {
		reached, err := b.runtime("List")
		if err != nil {
			yield(nil, err)
			return
		}
		options := listOptions{}
		for _, opt := range opts {
			opt.applyList(&options)
		}
		cursor := ""
		for {
			res, err := reached.client.List(ctx, &bucketv1.ListRequest{
				Bucket: reached.bucket,
				Prefix: options.prefix,
				Limit:  options.limit,
				Cursor: cursor,
			})
			if err != nil {
				yield(nil, err)
				return
			}
			for _, info := range res.GetObjects() {
				if !yield(objectFrom(info), nil) {
					return
				}
			}
			cursor = res.GetNextCursor()
			if cursor == "" {
				return
			}
		}
	}
}

// SignedURL is a url that reads the object under key without a credential, for
// as long as it stays valid.
func (b *BucketStore) SignedURL(ctx context.Context, key string, opts ...SignOption) (string, error) {
	options := signOptions{}
	for _, opt := range opts {
		opt.applySign(&options)
	}
	target, err := b.sign(ctx, "SignedURL", key,
		bucketv1.SignedOperation_SIGNED_OPERATION_GET,
		bucketv1.SignedAudience_SIGNED_AUDIENCE_EXTERNAL,
		&bucketv1.SignConstraints{DownloadFilename: options.download},
		options.expires,
	)
	if err != nil {
		return "", err
	}
	return target.GetUrl(), nil
}

// SignedUpload is a target that writes the object under key without a
// credential, for as long as it stays valid.
func (b *BucketStore) SignedUpload(ctx context.Context, key string, opts ...SignOption) (*SignedUpload, error) {
	options := signOptions{}
	for _, opt := range opts {
		opt.applySign(&options)
	}
	target, err := b.sign(ctx, "SignedUpload", key,
		bucketv1.SignedOperation_SIGNED_OPERATION_POST_UPLOAD,
		bucketv1.SignedAudience_SIGNED_AUDIENCE_EXTERNAL,
		&bucketv1.SignConstraints{ContentType: options.contentType, MaxSize: options.maxSize},
		options.expires,
	)
	if err != nil {
		return nil, err
	}
	method := target.GetMethod()
	if method == "" {
		method = http.MethodPost
	}
	upload := &SignedUpload{
		URL:     target.GetUrl(),
		Method:  method,
		Headers: target.GetHeaders(),
		Fields:  target.GetFields(),
	}
	if options.expires > 0 {
		upload.Expires = time.Now().Add(options.expires)
	}
	return upload, nil
}

// PublicURL is the address the object under key is served at anonymously. It
// fails on a bucket that has no public address.
func (b *BucketStore) PublicURL(key string) (*url.URL, error) {
	reached, err := b.runtime("PublicURL")
	if err != nil {
		return nil, err
	}
	if reached.publicBaseURL == "" {
		return nil, fmt.Errorf(
			"this bucket carries no public address, so %q has no public url: "+
				"declare the bucket with ocel.BucketPublic() and give the project a domain to serve it from",
			key,
		)
	}
	base, err := url.Parse(reached.publicBaseURL)
	if err != nil {
		return nil, fmt.Errorf("the public address this bucket was delivered is not a url: %w", err)
	}
	return base.JoinPath(key), nil
}

func (b *BucketStore) head(ctx context.Context, access, key string) (*Object, error) {
	reached, err := b.runtime(access)
	if err != nil {
		return nil, err
	}
	res, err := reached.client.Head(ctx, &bucketv1.HeadRequest{
		Bucket: reached.bucket,
		Key:    key,
	})
	if err != nil {
		return nil, refused(key, err)
	}
	info := res.GetObject()
	if info == nil {
		return nil, nil
	}
	return objectFrom(info), nil
}

func (b *BucketStore) sign(
	ctx context.Context,
	access, key string,
	operation bucketv1.SignedOperation,
	audience bucketv1.SignedAudience,
	constraints *bucketv1.SignConstraints,
	expires time.Duration,
) (*bucketv1.PresignedTarget, error) {
	reached, err := b.runtime(access)
	if err != nil {
		return nil, err
	}
	req := &bucketv1.SignRequest{
		Bucket:      reached.bucket,
		Key:         key,
		Operation:   operation,
		Audience:    audience,
		Constraints: constraints,
	}
	if expires > 0 {
		req.ExpiresIn = durationpb.New(expires)
	}
	res, err := reached.client.Sign(ctx, req)
	if err != nil {
		return nil, err
	}
	target := res.GetTarget()
	if target == nil {
		return nil, fmt.Errorf("the runtime signed nothing for %q", key)
	}
	return target, nil
}

func (b *BucketStore) runtime(access string) (*reachedBucket, error) {
	if discovering() {
		return nil, &UnprovisionedError{Resource: b.resource(), Access: access}
	}
	b.once.Do(func() {
		delivered, err := binding(b.name, bindingsv1.BindingType_BINDING_TYPE_BUCKET)
		if err != nil {
			b.err = err
			return
		}
		address := os.Getenv(runtimeAddressEnv)
		if address == "" {
			b.err = &unaddressedRuntimeError{}
			return
		}
		token := os.Getenv(sessionTokenEnv)
		if token == "" {
			b.err = &untokenedRuntimeError{}
			return
		}
		properties := delivered.GetBucket()
		b.reached = &reachedBucket{
			client: bucketv1connect.NewBucketServiceClient(b.http, address,
				connect.WithInterceptors(bearing(token))),
			bucket:        properties.GetBucket(),
			publicBaseURL: properties.GetPublicBaseUrl(),
		}
	})
	return b.reached, b.err
}

type unaddressedRuntimeError struct{}

func (*unaddressedRuntimeError) Error() string {
	return fmt.Sprintf(
		"%s is not defined, so no resource the ocel runtime serves can be reached. "+
			"Run `ocel dev` to serve it locally, or `ocel deploy` to have the deployed runtime's address delivered.",
		runtimeAddressEnv,
	)
}

type untokenedRuntimeError struct{}

func (*untokenedRuntimeError) Error() string {
	return fmt.Sprintf(
		"%s is not defined, so the ocel runtime at %s would refuse every call. "+
			"It is delivered beside %s by `ocel dev` and by the deployed runtime, never set by hand.",
		sessionTokenEnv, runtimeAddressEnv, runtimeAddressEnv,
	)
}

func (b *BucketStore) resource() string {
	return fmt.Sprintf("bucket(%q)", b.name)
}

func bearing(token string) connect.Interceptor {
	return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
		return func(ctx context.Context, req connect.AnyRequest) (connect.AnyResponse, error) {
			req.Header().Set("Authorization", bearer(token))
			return next(ctx, req)
		}
	})
}

func objectFrom(wire *bucketv1.ObjectInfo) *Object {
	obj := &Object{
		Key:         wire.GetKey(),
		Size:        wire.GetSize(),
		ETag:        wire.GetEtag(),
		ContentType: wire.GetContentType(),
		Metadata:    wire.GetMetadata(),
	}
	if at := wire.GetUploadedAt(); at != nil {
		obj.UploadedAt = at.AsTime()
	}
	return obj
}

func objectNotFound(key string) error {
	return fmt.Errorf("%w: %q", ErrObjectNotFound, key)
}

func preconditionFailed(key string) error {
	return fmt.Errorf("%w: %q", ErrPreconditionFailed, key)
}

func refused(key string, err error) error {
	switch connect.CodeOf(err) {
	case connect.CodeNotFound:
		return objectNotFound(key)
	case connect.CodeFailedPrecondition:
		return preconditionFailed(key)
	default:
		return err
	}
}

func refusedStatus(key string, status int) error {
	switch status {
	case http.StatusNotFound:
		return objectNotFound(key)
	case http.StatusPreconditionFailed, http.StatusConflict:
		return preconditionFailed(key)
	default:
		return nil
	}
}
