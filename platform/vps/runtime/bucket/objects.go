package bucket

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"

	connect "connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"
	"github.com/aws/smithy-go"
	"google.golang.org/protobuf/types/known/timestamppb"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

const listPageSize = 1000

func missing(err error) bool {
	var notFound *s3types.NotFound
	var noSuchKey *s3types.NoSuchKey
	if errors.As(err, &notFound) || errors.As(err, &noSuchKey) {
		return true
	}
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "NotFound", "NoSuchKey", "404":
			return true
		}
	}
	return false
}

func preconditionFailed(err error) bool {
	var apiErr smithy.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.ErrorCode() {
		case "PreconditionFailed", "412", "ConditionalRequestConflict":
			return true
		}
	}
	return false
}

func storeError(op string, err error) error {
	switch {
	case missing(err):
		return connect.NewError(connect.CodeNotFound, fmt.Errorf("%s: no object under that key", op))
	case preconditionFailed(err):
		return connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("%s: the object did not meet the condition the write carried", op))
	default:
		return connect.NewError(connect.CodeInternal, fmt.Errorf("%s: %w", op, err))
	}
}

func headInfo(key string, out *s3.HeadObjectOutput) *bucketv1.ObjectInfo {
	info := &bucketv1.ObjectInfo{
		Key:         key,
		Size:        aws.ToInt64(out.ContentLength),
		Etag:        aws.ToString(out.ETag),
		ContentType: aws.ToString(out.ContentType),
		Metadata:    out.Metadata,
	}
	if out.LastModified != nil {
		info.UploadedAt = timestamppb.New(*out.LastModified)
	}
	return info
}

// Head answers what the store knows about one key, or nothing where it holds none.
func (s *Service) Head(ctx context.Context, req *bucketv1.HeadRequest) (*bucketv1.HeadResponse, error) {
	held := scopeOf(req.GetBucket())
	out, err := s.cfg.Objects.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(held.bucket),
		Key:    aws.String(held.key(req.GetKey())),
	})
	if missing(err) {
		return &bucketv1.HeadResponse{}, nil
	}
	if err != nil {
		return nil, storeError("head "+req.GetKey(), err)
	}
	return &bucketv1.HeadResponse{Object: headInfo(req.GetKey(), out)}, nil
}

// List answers one page of the keys under a prefix, never the store's own bookkeeping.
func (s *Service) List(ctx context.Context, req *bucketv1.ListRequest) (*bucketv1.ListResponse, error) {
	held := scopeOf(req.GetBucket())
	limit := req.GetLimit()
	if limit <= 0 {
		limit = listPageSize
	}
	in := &s3.ListObjectsV2Input{
		Bucket:  aws.String(held.bucket),
		Prefix:  aws.String(held.key(req.GetPrefix())),
		MaxKeys: aws.Int32(limit),
	}
	if req.GetCursor() != "" {
		in.ContinuationToken = aws.String(req.GetCursor())
	}
	out, err := s.cfg.Objects.ListObjectsV2(ctx, in)
	if err != nil {
		return nil, storeError("list "+req.GetPrefix(), err)
	}

	resp := &bucketv1.ListResponse{NextCursor: aws.ToString(out.NextContinuationToken)}
	for _, obj := range out.Contents {
		key := held.strip(aws.ToString(obj.Key))
		if strings.HasPrefix(key, reservedPrefix) {
			continue
		}
		info := &bucketv1.ObjectInfo{
			Key:  key,
			Size: aws.ToInt64(obj.Size),
			Etag: aws.ToString(obj.ETag),
		}
		if obj.LastModified != nil {
			info.UploadedAt = timestamppb.New(*obj.LastModified)
		}
		resp.Objects = append(resp.Objects, info)
	}
	return resp, nil
}

// Delete removes the named keys; a key the store does not hold is not an error.
func (s *Service) Delete(ctx context.Context, req *bucketv1.DeleteRequest) (*bucketv1.DeleteResponse, error) {
	held := scopeOf(req.GetBucket())
	if err := s.remove(ctx, held, req.GetKeys()); err != nil {
		return nil, err
	}
	return &bucketv1.DeleteResponse{}, nil
}

func (s *Service) remove(ctx context.Context, held scope, keys []string) error {
	if len(keys) == 0 {
		return nil
	}
	ids := make([]s3types.ObjectIdentifier, 0, len(keys))
	for _, key := range keys {
		ids = append(ids, s3types.ObjectIdentifier{Key: aws.String(held.key(key))})
	}
	_, err := s.cfg.Objects.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(held.bucket),
		Delete: &s3types.Delete{Objects: ids, Quiet: aws.Bool(true)},
	})
	if err != nil {
		return storeError("delete", err)
	}
	return nil
}

// Copy duplicates one object within the bucket.
func (s *Service) Copy(ctx context.Context, req *bucketv1.CopyRequest) (*bucketv1.CopyResponse, error) {
	held := scopeOf(req.GetBucket())
	_, err := s.cfg.Objects.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(held.bucket),
		Key:        aws.String(held.key(req.GetDestinationKey())),
		CopySource: aws.String(copySource(held.bucket, held.key(req.GetSourceKey()))),
	})
	if err != nil {
		return nil, storeError("copy "+req.GetSourceKey(), err)
	}
	out, err := s.cfg.Objects.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(held.bucket),
		Key:    aws.String(held.key(req.GetDestinationKey())),
	})
	if err != nil {
		return nil, storeError("head "+req.GetDestinationKey(), err)
	}
	return &bucketv1.CopyResponse{Object: headInfo(req.GetDestinationKey(), out)}, nil
}

func copySource(bucket, key string) string {
	return (&url.URL{Path: bucket + "/" + key}).EscapedPath()
}
