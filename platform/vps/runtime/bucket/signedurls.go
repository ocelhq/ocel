package bucket

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"time"

	connect "connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

func ttlOf(d interface{ AsDuration() time.Duration }) time.Duration {
	if d == nil {
		return presignTTL
	}
	if held := d.AsDuration(); held > 0 {
		return held
	}
	return presignTTL
}

// Sign hands back a url the caller drives itself, bounded by the constraints it asked for.
func (s *Service) Sign(ctx context.Context, req *bucketv1.SignRequest) (*bucketv1.SignResponse, error) {
	signer, err := s.signer(req.GetAudience())
	if err != nil {
		return nil, err
	}
	held := scopeOf(req.GetBucket())
	key := held.key(req.GetKey())
	c := req.GetConstraints()

	ttl := presignTTL
	if req.GetExpiresIn() != nil {
		ttl = ttlOf(req.GetExpiresIn())
	}

	switch req.GetOperation() {
	case bucketv1.SignedOperation_SIGNED_OPERATION_GET:
		in := &s3.GetObjectInput{Bucket: aws.String(held.bucket), Key: aws.String(key)}
		if name := c.GetDownloadFilename(); name != "" {
			in.ResponseContentDisposition = aws.String(mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		}
		signed, err := signer.PresignGetObject(ctx, in, func(o *s3.PresignOptions) { o.Expires = ttl })
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sign a read of %q: %w", req.GetKey(), err))
		}
		return &bucketv1.SignResponse{Target: &bucketv1.PresignedTarget{
			Url:     signed.URL,
			Key:     req.GetKey(),
			Method:  signed.Method,
			Headers: vendorHeaders(signed.SignedHeader),
		}}, nil

	case bucketv1.SignedOperation_SIGNED_OPERATION_PUT:
		if err := s.roomToWrite(); err != nil {
			return nil, err
		}
		in := &s3.PutObjectInput{Bucket: aws.String(held.bucket), Key: aws.String(key)}
		if c.GetContentType() != "" {
			in.ContentType = aws.String(c.GetContentType())
		}
		if c.GetIfNoneMatch() != "" {
			in.IfNoneMatch = aws.String(c.GetIfNoneMatch())
		}
		if c.GetIfMatch() != "" {
			in.IfMatch = aws.String(c.GetIfMatch())
		}
		signed, err := signer.PresignPutObject(ctx, in, func(o *s3.PresignOptions) { o.Expires = ttl })
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sign a write of %q: %w", req.GetKey(), err))
		}
		return &bucketv1.SignResponse{Target: &bucketv1.PresignedTarget{
			Url:     signed.URL,
			Key:     req.GetKey(),
			Method:  signed.Method,
			Headers: vendorHeaders(signed.SignedHeader),
		}}, nil

	case bucketv1.SignedOperation_SIGNED_OPERATION_POST_UPLOAD:
		if err := s.roomToWrite(); err != nil {
			return nil, err
		}
		in := &s3.PutObjectInput{Bucket: aws.String(held.bucket), Key: aws.String(key)}
		if !s.cfg.PostPolicies {
			return s.Sign(ctx, &bucketv1.SignRequest{
				Bucket:      req.GetBucket(),
				Key:         req.GetKey(),
				Operation:   bucketv1.SignedOperation_SIGNED_OPERATION_PUT,
				Audience:    req.GetAudience(),
				ExpiresIn:   req.GetExpiresIn(),
				Constraints: c,
			})
		}
		conditions := []any{}
		if c.GetContentType() != "" {
			in.ContentType = aws.String(c.GetContentType())
			conditions = append(conditions, map[string]string{"Content-Type": c.GetContentType()})
		}
		if c.GetMaxSize() > 0 {
			conditions = append(conditions, []any{"content-length-range", 0, c.GetMaxSize()})
		}
		signed, err := signer.PresignPostObject(ctx, in, func(o *s3.PresignPostOptions) {
			o.Expires = ttl
			o.Conditions = conditions
		})
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sign a browser upload of %q: %w", req.GetKey(), err))
		}
		return &bucketv1.SignResponse{Target: &bucketv1.PresignedTarget{
			Url:    signed.URL,
			Key:    req.GetKey(),
			Method: "POST",
			Fields: signed.Values,
		}}, nil

	default:
		return nil, connect.NewError(connect.CodeInvalidArgument, errors.New("a signed url is asked for as a read, a write or a browser upload"))
	}
}

// CreateMultipart opens an upload a caller sends in parts.
func (s *Service) CreateMultipart(ctx context.Context, req *bucketv1.CreateMultipartRequest) (*bucketv1.CreateMultipartResponse, error) {
	if err := s.roomToWrite(); err != nil {
		return nil, err
	}
	held := scopeOf(req.GetBucket())
	in := &s3.CreateMultipartUploadInput{
		Bucket:   aws.String(held.bucket),
		Key:      aws.String(held.key(req.GetKey())),
		Metadata: req.GetMetadata(),
	}
	if req.GetContentType() != "" {
		in.ContentType = aws.String(req.GetContentType())
	}
	if req.GetCacheControl() != "" {
		in.CacheControl = aws.String(req.GetCacheControl())
	}
	out, err := s.cfg.Objects.CreateMultipartUpload(ctx, in)
	if err != nil {
		return nil, storeError("open a multipart upload of "+req.GetKey(), err)
	}
	return &bucketv1.CreateMultipartResponse{UploadId: aws.ToString(out.UploadId)}, nil
}

// SignParts signs the batch of parts a caller is about to send.
func (s *Service) SignParts(ctx context.Context, req *bucketv1.SignPartsRequest) (*bucketv1.SignPartsResponse, error) {
	signer, err := s.signer(req.GetAudience())
	if err != nil {
		return nil, err
	}
	held := scopeOf(req.GetBucket())
	ttl := presignTTL
	if req.GetExpiresIn() != nil {
		ttl = ttlOf(req.GetExpiresIn())
	}

	resp := &bucketv1.SignPartsResponse{Parts: make([]*bucketv1.SignedPart, 0, len(req.GetPartNumbers()))}
	for _, number := range req.GetPartNumbers() {
		signed, err := signer.PresignUploadPart(ctx, &s3.UploadPartInput{
			Bucket:     aws.String(held.bucket),
			Key:        aws.String(held.key(req.GetKey())),
			UploadId:   aws.String(req.GetUploadId()),
			PartNumber: aws.Int32(number),
		}, func(o *s3.PresignOptions) { o.Expires = ttl })
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sign part %d of %q: %w", number, req.GetKey(), err))
		}
		resp.Parts = append(resp.Parts, &bucketv1.SignedPart{
			PartNumber: number,
			Url:        signed.URL,
			Headers:    vendorHeaders(signed.SignedHeader),
		})
	}
	return resp, nil
}

// CompleteMultipart assembles the parts a caller sent into the object.
func (s *Service) CompleteMultipart(ctx context.Context, req *bucketv1.CompleteMultipartRequest) (*bucketv1.CompleteMultipartResponse, error) {
	held := scopeOf(req.GetBucket())
	parts := make([]s3types.CompletedPart, 0, len(req.GetParts()))
	for _, part := range req.GetParts() {
		parts = append(parts, s3types.CompletedPart{
			PartNumber: aws.Int32(part.GetPartNumber()),
			ETag:       aws.String(part.GetEtag()),
		})
	}
	in := &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(held.bucket),
		Key:             aws.String(held.key(req.GetKey())),
		UploadId:        aws.String(req.GetUploadId()),
		MultipartUpload: &s3types.CompletedMultipartUpload{Parts: parts},
	}
	if req.GetIfNoneMatch() != "" {
		in.IfNoneMatch = aws.String(req.GetIfNoneMatch())
	}
	if req.GetIfMatch() != "" {
		in.IfMatch = aws.String(req.GetIfMatch())
	}
	if _, err := s.cfg.Objects.CompleteMultipartUpload(ctx, in); err != nil {
		return nil, storeError("assemble "+req.GetKey(), err)
	}
	out, err := s.cfg.Objects.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(held.bucket),
		Key:    aws.String(held.key(req.GetKey())),
	})
	if err != nil {
		return nil, storeError("head "+req.GetKey(), err)
	}
	return &bucketv1.CompleteMultipartResponse{Object: headInfo(req.GetKey(), out)}, nil
}

// AbortMultipart abandons an upload so its parts stop costing storage.
func (s *Service) AbortMultipart(ctx context.Context, req *bucketv1.AbortMultipartRequest) (*bucketv1.AbortMultipartResponse, error) {
	held := scopeOf(req.GetBucket())
	_, err := s.cfg.Objects.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(held.bucket),
		Key:      aws.String(held.key(req.GetKey())),
		UploadId: aws.String(req.GetUploadId()),
	})
	if err != nil && !missing(err) {
		return nil, storeError("abandon the multipart upload of "+req.GetKey(), err)
	}
	return &bucketv1.AbortMultipartResponse{}, nil
}
