package s3

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"sync"

	connect "connectrpc.com/connect"

	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
)

type router struct {
	own   bucketv1connect.BucketServiceHandler
	bound func() ([]*Service, error)
}

var _ bucketv1connect.BucketServiceHandler = (*router)(nil)

func route(own bucketv1connect.BucketServiceHandler, bound ...*Service) bucketv1connect.BucketServiceHandler {
	return &router{own: own, bound: func() ([]*Service, error) { return bound, nil }}
}

func RouteRecords(own bucketv1connect.BucketServiceHandler, records Records, callbacks Poster) bucketv1connect.BucketServiceHandler {
	var mu sync.Mutex
	var ready bool
	var seen uint32
	var read string
	var built []*Service
	return &router{own: own, bound: func() ([]*Service, error) {
		mu.Lock()
		defer mu.Unlock()
		generation := records.Generation()
		if ready && generation == seen {
			return built, nil
		}
		current := bucketRecords(records)
		if !ready || current != read {
			fresh, err := backends(records, callbacks)
			if err != nil {
				return nil, connect.NewError(connect.CodeFailedPrecondition, err)
			}
			read, built = current, fresh
		}
		ready, seen = true, generation
		return built, nil
	}}
}

func bucketRecords(records Records) string {
	var values strings.Builder
	for _, l := range records.Bindings() {
		if l.Type == bindingsv1.BindingType_BINDING_TYPE_BUCKET {
			values.WriteString(records.Value(l.Key))
			values.WriteByte(0)
		}
	}
	return values.String()
}

func (r *router) forBucket(name string) (bucketv1connect.BucketServiceHandler, error) {
	bound, err := r.bound()
	if err != nil {
		return nil, err
	}
	for _, backend := range bound {
		if backend.hasBucket(name) {
			return backend, nil
		}
	}
	if r.own == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, fmt.Errorf("this runtime has no bucket store of its own and serves only buckets bound to a store by endpoint, and %q is not one: bind it with an endpoint under `bindings.bucket`", name))
	}
	return r.own, nil
}

func (r *router) forSession(id string) (bucketv1connect.BucketServiceHandler, error) {
	bound, err := r.bound()
	if err != nil {
		return nil, err
	}
	for _, backend := range bound {
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
