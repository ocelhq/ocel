package bucket

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"golang.org/x/sync/errgroup"
	"google.golang.org/api/googleapi"
	storage "google.golang.org/api/storage/v1"

	s3store "github.com/ocelhq/ocel/platform/s3"
)

const (
	objectsDeletedAtOnce = 16
	noACL                = "noAcl"
	generationAnswer     = "X-Goog-Generation"
)

type objects struct {
	service   *storage.Service
	multipart multipart
}

var _ s3store.ObjectAPI = objects{}

func formatGeneration(generation int64) string { return strconv.FormatInt(generation, 10) }

func etagOf(generation int64) string { return `"` + formatGeneration(generation) + `"` }

func generationOf(etag string) (int64, error) {
	generation, err := strconv.ParseInt(strings.Trim(strings.TrimSpace(etag), `"`), 10, 64)
	if err != nil {
		return 0, &smithy.GenericAPIError{Code: "PreconditionFailed", Message: fmt.Sprintf("%q names no generation of an object in this bucket", etag)}
	}
	return generation, nil
}

func generationMatch(ifNoneMatch, ifMatch *string) (string, error) {
	switch {
	case aws.ToString(ifNoneMatch) == "*":
		return "0", nil
	case aws.ToString(ifMatch) != "":
		generation, err := generationOf(aws.ToString(ifMatch))
		if err != nil {
			return "", err
		}
		return formatGeneration(generation), nil
	}
	return "", nil
}

var s3Codes = map[int]string{
	http.StatusNotFound:           "NotFound",
	http.StatusPreconditionFailed: "PreconditionFailed",
}

func storeError(err error) error {
	var answered *googleapi.Error
	if errors.As(err, &answered) {
		if code, known := s3Codes[answered.Code]; known {
			return &smithy.GenericAPIError{Code: code, Message: answered.Message}
		}
	}
	return err
}

func asked[T any](ctx context.Context, call func(...googleapi.CallOption) (T, error)) (T, error) {
	value, err := retried(ctx, func() (T, int, error) {
		value, err := call()
		return value, answeredStatus(err), err
	})
	return value, storeError(err)
}

func timeOf(stamp string) *time.Time {
	at, err := time.Parse(time.RFC3339Nano, stamp)
	if err != nil {
		return nil
	}
	return &at
}

func (o objects) HeadObject(ctx context.Context, in *s3.HeadObjectInput, _ ...func(*s3.Options)) (*s3.HeadObjectOutput, error) {
	object, err := asked(ctx, o.service.Objects.Get(aws.ToString(in.Bucket), aws.ToString(in.Key)).Projection(noACL).Context(ctx).Do)
	if err != nil {
		return nil, err
	}
	return &s3.HeadObjectOutput{
		ContentLength: aws.Int64(int64(object.Size)),
		ContentType:   aws.String(object.ContentType),
		CacheControl:  aws.String(object.CacheControl),
		ETag:          aws.String(etagOf(object.Generation)),
		LastModified:  timeOf(object.TimeCreated),
		Metadata:      object.Metadata,
	}, nil
}

func (o objects) GetObject(ctx context.Context, in *s3.GetObjectInput, _ ...func(*s3.Options)) (*s3.GetObjectOutput, error) {
	resp, err := asked(ctx, o.service.Objects.Get(aws.ToString(in.Bucket), aws.ToString(in.Key)).Context(ctx).Download)
	if err != nil {
		return nil, err
	}
	generation, _ := strconv.ParseInt(resp.Header.Get(generationAnswer), 10, 64)
	return &s3.GetObjectOutput{
		Body:          resp.Body,
		ContentLength: aws.Int64(resp.ContentLength),
		ContentType:   aws.String(resp.Header.Get("Content-Type")),
		ETag:          aws.String(etagOf(generation)),
	}, nil
}

func (o objects) PutObject(ctx context.Context, in *s3.PutObjectInput, _ ...func(*s3.Options)) (*s3.PutObjectOutput, error) {
	match, err := generationMatch(in.IfNoneMatch, in.IfMatch)
	if err != nil {
		return nil, err
	}
	var body []byte
	if in.Body != nil {
		if body, err = io.ReadAll(in.Body); err != nil {
			return nil, err
		}
	}
	described := &storage.Object{
		Name:               aws.ToString(in.Key),
		ContentType:        aws.ToString(in.ContentType),
		CacheControl:       aws.ToString(in.CacheControl),
		ContentDisposition: aws.ToString(in.ContentDisposition),
		Metadata:           in.Metadata,
	}
	written, err := asked(ctx, func(...googleapi.CallOption) (*storage.Object, error) {
		call := o.service.Objects.Insert(aws.ToString(in.Bucket), described).
			Media(bytes.NewReader(body), googleapi.ContentType(described.ContentType)).Projection(noACL).Context(ctx)
		if match != "" {
			generation, _ := strconv.ParseInt(match, 10, 64)
			call = call.IfGenerationMatch(generation)
		}
		return call.Do()
	})
	if err != nil {
		return nil, err
	}
	return &s3.PutObjectOutput{ETag: aws.String(etagOf(written.Generation))}, nil
}

func (o objects) ListObjectsV2(ctx context.Context, in *s3.ListObjectsV2Input, _ ...func(*s3.Options)) (*s3.ListObjectsV2Output, error) {
	call := o.service.Objects.List(aws.ToString(in.Bucket)).Prefix(aws.ToString(in.Prefix)).Projection(noACL).Context(ctx)
	if limit := aws.ToInt32(in.MaxKeys); limit > 0 {
		call = call.MaxResults(int64(limit))
	}
	if token := aws.ToString(in.ContinuationToken); token != "" {
		call = call.PageToken(token)
	}
	listed, err := asked(ctx, call.Do)
	if err != nil {
		return nil, err
	}
	out := &s3.ListObjectsV2Output{}
	if listed.NextPageToken != "" {
		out.NextContinuationToken = aws.String(listed.NextPageToken)
		out.IsTruncated = aws.Bool(true)
	}
	for _, object := range listed.Items {
		out.Contents = append(out.Contents, s3types.Object{
			Key:          aws.String(object.Name),
			Size:         aws.Int64(int64(object.Size)),
			ETag:         aws.String(etagOf(object.Generation)),
			LastModified: timeOf(object.TimeCreated),
		})
	}
	return out, nil
}

func missing(err error) bool {
	var refused *smithy.GenericAPIError
	return errors.As(err, &refused) && refused.Code == "NotFound"
}

func (o objects) DeleteObjects(ctx context.Context, in *s3.DeleteObjectsInput, _ ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error) {
	bucket := aws.ToString(in.Bucket)
	deleting, ctx := errgroup.WithContext(ctx)
	deleting.SetLimit(objectsDeletedAtOnce)
	for _, id := range in.Delete.Objects {
		deleting.Go(func() error {
			_, err := asked(ctx, func(...googleapi.CallOption) (struct{}, error) {
				return struct{}{}, o.service.Objects.Delete(bucket, aws.ToString(id.Key)).Context(ctx).Do()
			})
			if err == nil || missing(err) {
				return nil
			}
			return err
		})
	}
	if err := deleting.Wait(); err != nil {
		return nil, err
	}
	return &s3.DeleteObjectsOutput{}, nil
}

func (o objects) CopyObject(ctx context.Context, in *s3.CopyObjectInput, _ ...func(*s3.Options)) (*s3.CopyObjectOutput, error) {
	source, err := url.PathUnescape(aws.ToString(in.CopySource))
	if err != nil {
		return nil, fmt.Errorf("read the copy source %q: %w", aws.ToString(in.CopySource), err)
	}
	sourceBucket, sourceKey, found := strings.Cut(strings.TrimPrefix(source, "/"), "/")
	if !found {
		return nil, fmt.Errorf("the copy source %q names no object", source)
	}
	token := ""
	for {
		call := o.service.Objects.Rewrite(sourceBucket, sourceKey, aws.ToString(in.Bucket), aws.ToString(in.Key), &storage.Object{}).Context(ctx)
		if token != "" {
			call = call.RewriteToken(token)
		}
		rewritten, err := asked(ctx, call.Do)
		if err != nil {
			return nil, err
		}
		if rewritten.Done && rewritten.Resource != nil {
			return &s3.CopyObjectOutput{CopyObjectResult: &s3types.CopyObjectResult{ETag: aws.String(etagOf(rewritten.Resource.Generation))}}, nil
		}
		if token = rewritten.RewriteToken; token == "" {
			return nil, fmt.Errorf("copy %s: Cloud Storage answered an unfinished copy with no token to continue it", sourceKey)
		}
	}
}

func (o objects) CreateMultipartUpload(ctx context.Context, in *s3.CreateMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error) {
	id, err := o.multipart.open(ctx, aws.ToString(in.Bucket), aws.ToString(in.Key), aws.ToString(in.ContentType), aws.ToString(in.CacheControl), in.Metadata)
	if err != nil {
		return nil, err
	}
	return &s3.CreateMultipartUploadOutput{UploadId: aws.String(id)}, nil
}

func (o objects) CompleteMultipartUpload(ctx context.Context, in *s3.CompleteMultipartUploadInput, _ ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error) {
	match, err := generationMatch(in.IfNoneMatch, in.IfMatch)
	if err != nil {
		return nil, err
	}
	var parts []s3types.CompletedPart
	if in.MultipartUpload != nil {
		parts = in.MultipartUpload.Parts
	}
	if err := o.multipart.assemble(ctx, aws.ToString(in.Bucket), aws.ToString(in.Key), aws.ToString(in.UploadId), parts, match); err != nil {
		return nil, err
	}
	return &s3.CompleteMultipartUploadOutput{}, nil
}

func (o objects) AbortMultipartUpload(ctx context.Context, in *s3.AbortMultipartUploadInput, _ ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error) {
	if err := o.multipart.abort(ctx, aws.ToString(in.Bucket), aws.ToString(in.Key), aws.ToString(in.UploadId)); err != nil {
		return nil, err
	}
	return &s3.AbortMultipartUploadOutput{}, nil
}

func (o objects) ListMultipartUploads(context.Context, *s3.ListMultipartUploadsInput, ...func(*s3.Options)) (*s3.ListMultipartUploadsOutput, error) {
	return nil, errors.New("a Cloud Storage bucket aborts abandoned multipart uploads by its lifecycle rule, so the runtime never lists them")
}
