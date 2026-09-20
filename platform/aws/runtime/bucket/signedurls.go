package bucket

import (
	"context"
	"errors"
	"fmt"
	"mime"
	"strings"

	connect "connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	v4 "github.com/aws/aws-sdk-go-v2/aws/signer/v4"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	s3types "github.com/aws/aws-sdk-go-v2/service/s3/types"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
)

func vendorHeaders(signed map[string][]string) map[string]string {
	headers := map[string]string{}
	for name, values := range signed {
		if lower := strings.ToLower(name); strings.HasPrefix(lower, vendorHeaderPrefix) && len(values) > 0 {
			headers[lower] = values[0]
		}
	}
	return headers
}

func (s *Service) Sign(ctx context.Context, req *bucketv1.SignRequest) (*bucketv1.SignResponse, error) {
	if err := s.reach(req.GetBucket(), req.GetKey()); err != nil {
		return nil, err
	}
	ttl := presignTTL
	if req.GetExpiresIn() != nil && req.GetExpiresIn().AsDuration() > 0 {
		ttl = req.GetExpiresIn().AsDuration()
	}
	c := req.GetConstraints()

	switch req.GetOperation() {
	case bucketv1.SignedOperation_SIGNED_OPERATION_GET:
		in := &s3.GetObjectInput{Bucket: aws.String(req.GetBucket()), Key: aws.String(req.GetKey())}
		if name := c.GetDownloadFilename(); name != "" {
			in.ResponseContentDisposition = aws.String(mime.FormatMediaType("attachment", map[string]string{"filename": name}))
		}
		signed, err := s.presigner.PresignGetObject(ctx, in, func(o *s3.PresignOptions) { o.Expires = ttl })
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sign a read of %q: %w", req.GetKey(), err))
		}
		return &bucketv1.SignResponse{Target: target(req.GetKey(), signed)}, nil

	case bucketv1.SignedOperation_SIGNED_OPERATION_PUT:
		in := &s3.PutObjectInput{Bucket: aws.String(req.GetBucket()), Key: aws.String(req.GetKey())}
		if c.GetContentType() != "" {
			in.ContentType = aws.String(c.GetContentType())
		}
		if c.GetIfNoneMatch() != "" {
			in.IfNoneMatch = aws.String(c.GetIfNoneMatch())
		}
		if c.GetIfMatch() != "" {
			in.IfMatch = aws.String(c.GetIfMatch())
		}
		signed, err := s.presigner.PresignPutObject(ctx, in, func(o *s3.PresignOptions) { o.Expires = ttl })
		if err != nil {
			return nil, connect.NewError(connect.CodeInternal, fmt.Errorf("sign a write of %q: %w", req.GetKey(), err))
		}
		return &bucketv1.SignResponse{Target: target(req.GetKey(), signed)}, nil

	case bucketv1.SignedOperation_SIGNED_OPERATION_POST_UPLOAD:
		in := &s3.PutObjectInput{Bucket: aws.String(req.GetBucket()), Key: aws.String(req.GetKey())}
		conditions := []any{}
		if c.GetContentType() != "" {
			in.ContentType = aws.String(c.GetContentType())
			conditions = append(conditions, map[string]string{"Content-Type": c.GetContentType()})
		}
		if c.GetMaxSize() > 0 {
			conditions = append(conditions, []any{"content-length-range", 0, c.GetMaxSize()})
		}
		signed, err := s.presigner.PresignPostObject(ctx, in, func(o *s3.PresignPostOptions) {
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

func target(key string, signed *v4.PresignedHTTPRequest) *bucketv1.PresignedTarget {
	return &bucketv1.PresignedTarget{
		Url:     signed.URL,
		Key:     key,
		Method:  signed.Method,
		Headers: vendorHeaders(signed.SignedHeader),
	}
}

func (s *Service) CreateMultipart(ctx context.Context, req *bucketv1.CreateMultipartRequest) (*bucketv1.CreateMultipartResponse, error) {
	if err := s.reach(req.GetBucket(), req.GetKey()); err != nil {
		return nil, err
	}
	in := &s3.CreateMultipartUploadInput{
		Bucket:   aws.String(req.GetBucket()),
		Key:      aws.String(req.GetKey()),
		Metadata: req.GetMetadata(),
	}
	if req.GetContentType() != "" {
		in.ContentType = aws.String(req.GetContentType())
	}
	if req.GetCacheControl() != "" {
		in.CacheControl = aws.String(req.GetCacheControl())
	}
	out, err := s.objects.CreateMultipartUpload(ctx, in)
	if err != nil {
		return nil, storeError("open a multipart upload of "+req.GetKey(), err)
	}
	return &bucketv1.CreateMultipartResponse{UploadId: aws.ToString(out.UploadId)}, nil
}

func (s *Service) SignParts(ctx context.Context, req *bucketv1.SignPartsRequest) (*bucketv1.SignPartsResponse, error) {
	if err := s.reach(req.GetBucket(), req.GetKey()); err != nil {
		return nil, err
	}
	ttl := presignTTL
	if req.GetExpiresIn() != nil && req.GetExpiresIn().AsDuration() > 0 {
		ttl = req.GetExpiresIn().AsDuration()
	}
	resp := &bucketv1.SignPartsResponse{Parts: make([]*bucketv1.SignedPart, 0, len(req.GetPartNumbers()))}
	for _, number := range req.GetPartNumbers() {
		signed, err := s.presigner.PresignUploadPart(ctx, &s3.UploadPartInput{
			Bucket:     aws.String(req.GetBucket()),
			Key:        aws.String(req.GetKey()),
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

func (s *Service) CompleteMultipart(ctx context.Context, req *bucketv1.CompleteMultipartRequest) (*bucketv1.CompleteMultipartResponse, error) {
	if err := s.reach(req.GetBucket(), req.GetKey()); err != nil {
		return nil, err
	}
	parts := make([]s3types.CompletedPart, 0, len(req.GetParts()))
	for _, part := range req.GetParts() {
		parts = append(parts, s3types.CompletedPart{
			PartNumber: aws.Int32(part.GetPartNumber()),
			ETag:       aws.String(part.GetEtag()),
		})
	}
	in := &s3.CompleteMultipartUploadInput{
		Bucket:          aws.String(req.GetBucket()),
		Key:             aws.String(req.GetKey()),
		UploadId:        aws.String(req.GetUploadId()),
		MultipartUpload: &s3types.CompletedMultipartUpload{Parts: parts},
	}
	if req.GetIfNoneMatch() != "" {
		in.IfNoneMatch = aws.String(req.GetIfNoneMatch())
	}
	if req.GetIfMatch() != "" {
		in.IfMatch = aws.String(req.GetIfMatch())
	}
	if _, err := s.objects.CompleteMultipartUpload(ctx, in); err != nil {
		return nil, storeError("assemble "+req.GetKey(), err)
	}
	head, err := s.objects.HeadObject(ctx, &s3.HeadObjectInput{
		Bucket: aws.String(req.GetBucket()),
		Key:    aws.String(req.GetKey()),
	})
	if err != nil {
		return nil, storeError("head "+req.GetKey(), err)
	}
	return &bucketv1.CompleteMultipartResponse{Object: headInfo(req.GetKey(), head)}, nil
}

func (s *Service) AbortMultipart(ctx context.Context, req *bucketv1.AbortMultipartRequest) (*bucketv1.AbortMultipartResponse, error) {
	if err := s.reach(req.GetBucket(), req.GetKey()); err != nil {
		return nil, err
	}
	_, err := s.objects.AbortMultipartUpload(ctx, &s3.AbortMultipartUploadInput{
		Bucket:   aws.String(req.GetBucket()),
		Key:      aws.String(req.GetKey()),
		UploadId: aws.String(req.GetUploadId()),
	})
	if err != nil && !missing(err) {
		return nil, storeError("abandon the multipart upload of "+req.GetKey(), err)
	}
	return &bucketv1.AbortMultipartResponse{}, nil
}

func (s *Service) CompleteUpload(ctx context.Context, req *bucketv1.CompleteUploadRequest) (*bucketv1.CompleteUploadResponse, error) {
	status, err := s.GetUploadStatus(ctx, &bucketv1.GetUploadStatusRequest{SessionId: req.GetSessionId()})
	if err != nil {
		return nil, err
	}
	return &bucketv1.CompleteUploadResponse{State: status.GetState(), Error: status.GetError()}, nil
}
