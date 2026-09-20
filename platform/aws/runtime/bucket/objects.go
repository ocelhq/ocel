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

	"github.com/ocelhq/ocel/pkg/constants"
	"google.golang.org/protobuf/types/known/timestamppb"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

const listPageSize = 1000

type objectAPI interface {
	HeadObject(context.Context, *s3.HeadObjectInput, ...func(*s3.Options)) (*s3.HeadObjectOutput, error)
	ListObjectsV2(context.Context, *s3.ListObjectsV2Input, ...func(*s3.Options)) (*s3.ListObjectsV2Output, error)
	DeleteObjects(context.Context, *s3.DeleteObjectsInput, ...func(*s3.Options)) (*s3.DeleteObjectsOutput, error)
	CopyObject(context.Context, *s3.CopyObjectInput, ...func(*s3.Options)) (*s3.CopyObjectOutput, error)
	CreateMultipartUpload(context.Context, *s3.CreateMultipartUploadInput, ...func(*s3.Options)) (*s3.CreateMultipartUploadOutput, error)
	CompleteMultipartUpload(context.Context, *s3.CompleteMultipartUploadInput, ...func(*s3.Options)) (*s3.CompleteMultipartUploadOutput, error)
	AbortMultipartUpload(context.Context, *s3.AbortMultipartUploadInput, ...func(*s3.Options)) (*s3.AbortMultipartUploadOutput, error)
}

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

func copySource(bucket, key string) string {
	return (&url.URL{Path: bucket + "/" + key}).EscapedPath()
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

func (s *Service) Head(ctx context.Context, req *bucketv1.HeadRequest) (*bucketv1.HeadResponse, error) {
	out, err := s.objects.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(req.GetBucket()),
		Key:    aws.String(req.GetKey()),
	})
	if missing(err) {
		return &bucketv1.HeadResponse{}, nil
	}
	if err != nil {
		return nil, storeError("head "+req.GetKey(), err)
	}
	return &bucketv1.HeadResponse{Object: headInfo(req.GetKey(), out)}, nil
}

func (s *Service) List(ctx context.Context, req *bucketv1.ListRequest) (*bucketv1.ListResponse, error) {
	limit := req.GetLimit()
	if limit <= 0 {
		limit = listPageSize
	}
	in := &s3.ListObjectsV2Input{
		Bucket:  aws.String(req.GetBucket()),
		Prefix:  aws.String(req.GetPrefix()),
		MaxKeys: aws.Int32(limit),
	}
	if req.GetCursor() != "" {
		in.ContinuationToken = aws.String(req.GetCursor())
	}
	out, err := s.objects.ListObjectsV2(ctx, in)
	if err != nil {
		return nil, storeError("list "+req.GetPrefix(), err)
	}

	resp := &bucketv1.ListResponse{NextCursor: aws.ToString(out.NextContinuationToken)}
	for _, obj := range out.Contents {
		key := aws.ToString(obj.Key)
		if strings.HasPrefix(key, constants.ReservedKeyPrefix) {
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

func (s *Service) Delete(ctx context.Context, req *bucketv1.DeleteRequest) (*bucketv1.DeleteResponse, error) {
	ids := make([]s3types.ObjectIdentifier, 0, len(req.GetKeys()))
	for _, key := range req.GetKeys() {
		ids = append(ids, s3types.ObjectIdentifier{Key: aws.String(key)})
	}
	_, err := s.objects.DeleteObjects(ctx, &s3.DeleteObjectsInput{
		Bucket: aws.String(req.GetBucket()),
		Delete: &s3types.Delete{Objects: ids, Quiet: aws.Bool(true)},
	})
	if err != nil {
		return nil, storeError("delete", err)
	}
	return &bucketv1.DeleteResponse{}, nil
}

func (s *Service) Copy(ctx context.Context, req *bucketv1.CopyRequest) (*bucketv1.CopyResponse, error) {
	_, err := s.objects.CopyObject(ctx, &s3.CopyObjectInput{
		Bucket:     aws.String(req.GetBucket()),
		Key:        aws.String(req.GetDestinationKey()),
		CopySource: aws.String(copySource(req.GetBucket(), req.GetSourceKey())),
	})
	if err != nil {
		return nil, storeError("copy "+req.GetSourceKey(), err)
	}
	head, err := s.objects.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(req.GetBucket()),
		Key:    aws.String(req.GetDestinationKey()),
	})
	if err != nil {
		return nil, storeError("head "+req.GetDestinationKey(), err)
	}
	return &bucketv1.CopyResponse{Object: headInfo(req.GetDestinationKey(), head)}, nil
}
