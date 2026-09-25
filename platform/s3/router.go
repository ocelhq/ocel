package s3

import (
	"context"
	"errors"
	"fmt"

	connect "connectrpc.com/connect"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
)

type router struct {
	own   bucketv1connect.BucketServiceHandler
	bound []*Service
}

var _ bucketv1connect.BucketServiceHandler = (*router)(nil)

func Route(own bucketv1connect.BucketServiceHandler, bound ...*Service) bucketv1connect.BucketServiceHandler {
	return &router{own: own, bound: bound}
}

func (r *router) forBucket(name string) (bucketv1connect.BucketServiceHandler, error) {
	for _, backend := range r.bound {
		if backend.holds(name) {
			return backend, nil
		}
	}
	if r.own == nil {
		return nil, connect.NewError(connect.CodePermissionDenied, fmt.Errorf("this app was granted no bucket called %q", name))
	}
	return r.own, nil
}

func (r *router) forSession(id string) (bucketv1connect.BucketServiceHandler, error) {
	for _, backend := range r.bound {
		if backend.opened(id) {
			return backend, nil
		}
	}
	if r.own == nil {
		return nil, connect.NewError(connect.CodeNotFound, errors.New("session not found"))
	}
	return r.own, nil
}

func (r *router) PresignUpload(ctx context.Context, req *bucketv1.PresignUploadRequest) (*bucketv1.PresignUploadResponse, error) {
	backend, err := r.forBucket(req.GetBucket())
	if err != nil {
		return nil, err
	}
	return backend.PresignUpload(ctx, req)
}

func (r *router) VerifyUploadSignature(ctx context.Context, req *bucketv1.VerifyUploadSignatureRequest) (*bucketv1.VerifyUploadSignatureResponse, error) {
	backend, err := r.forSession(req.GetSessionId())
	if err != nil {
		return nil, err
	}
	return backend.VerifyUploadSignature(ctx, req)
}

func (r *router) GetUploadStatus(ctx context.Context, req *bucketv1.GetUploadStatusRequest) (*bucketv1.GetUploadStatusResponse, error) {
	backend, err := r.forSession(req.GetSessionId())
	if err != nil {
		return nil, err
	}
	return backend.GetUploadStatus(ctx, req)
}

func (r *router) CompleteUpload(ctx context.Context, req *bucketv1.CompleteUploadRequest) (*bucketv1.CompleteUploadResponse, error) {
	backend, err := r.forSession(req.GetSessionId())
	if err != nil {
		return nil, err
	}
	return backend.CompleteUpload(ctx, req)
}

func (r *router) Head(ctx context.Context, req *bucketv1.HeadRequest) (*bucketv1.HeadResponse, error) {
	backend, err := r.forBucket(req.GetBucket())
	if err != nil {
		return nil, err
	}
	return backend.Head(ctx, req)
}

func (r *router) List(ctx context.Context, req *bucketv1.ListRequest) (*bucketv1.ListResponse, error) {
	backend, err := r.forBucket(req.GetBucket())
	if err != nil {
		return nil, err
	}
	return backend.List(ctx, req)
}

func (r *router) Delete(ctx context.Context, req *bucketv1.DeleteRequest) (*bucketv1.DeleteResponse, error) {
	backend, err := r.forBucket(req.GetBucket())
	if err != nil {
		return nil, err
	}
	return backend.Delete(ctx, req)
}

func (r *router) Copy(ctx context.Context, req *bucketv1.CopyRequest) (*bucketv1.CopyResponse, error) {
	backend, err := r.forBucket(req.GetBucket())
	if err != nil {
		return nil, err
	}
	return backend.Copy(ctx, req)
}

func (r *router) Sign(ctx context.Context, req *bucketv1.SignRequest) (*bucketv1.SignResponse, error) {
	backend, err := r.forBucket(req.GetBucket())
	if err != nil {
		return nil, err
	}
	return backend.Sign(ctx, req)
}

func (r *router) CreateMultipart(ctx context.Context, req *bucketv1.CreateMultipartRequest) (*bucketv1.CreateMultipartResponse, error) {
	backend, err := r.forBucket(req.GetBucket())
	if err != nil {
		return nil, err
	}
	return backend.CreateMultipart(ctx, req)
}

func (r *router) SignParts(ctx context.Context, req *bucketv1.SignPartsRequest) (*bucketv1.SignPartsResponse, error) {
	backend, err := r.forBucket(req.GetBucket())
	if err != nil {
		return nil, err
	}
	return backend.SignParts(ctx, req)
}

func (r *router) CompleteMultipart(ctx context.Context, req *bucketv1.CompleteMultipartRequest) (*bucketv1.CompleteMultipartResponse, error) {
	backend, err := r.forBucket(req.GetBucket())
	if err != nil {
		return nil, err
	}
	return backend.CompleteMultipart(ctx, req)
}

func (r *router) AbortMultipart(ctx context.Context, req *bucketv1.AbortMultipartRequest) (*bucketv1.AbortMultipartResponse, error) {
	backend, err := r.forBucket(req.GetBucket())
	if err != nil {
		return nil, err
	}
	return backend.AbortMultipart(ctx, req)
}
