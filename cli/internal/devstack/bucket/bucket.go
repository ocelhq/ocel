package bucket

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"maps"
	"net/http"
	"regexp"
	"slices"
	"strconv"
	"strings"
	"sync"
	"time"

	connect "connectrpc.com/connect"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
	"github.com/ocelhq/ocel/cli/internal/resolve"
	"github.com/ocelhq/ocel/pkg/constants"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

const (
	component = "bucket"

	storePort   = 9000
	dataPath    = "/data"
	healthPath  = "/health/ready"
	readyIn     = 2 * time.Minute
	accessKeyID = "ocel"
	storeRegion = "local"
)

var _ bucketv1connect.BucketServiceHandler = (*Component)(nil)

type Component struct {
	open       docker.Opener
	stateDir   string
	appOrigins func() []string

	mu        sync.Mutex
	container *docker.Container
	store     *s3store.Store
	buckets   map[string][]string
	service   *s3store.Service
}

func New(open docker.Opener, stateDir string, appOrigins func() []string) *Component {
	return &Component{open: open, stateDir: stateDir, appOrigins: appOrigins, buckets: map[string][]string{}}
}

func (c *Component) Resolve(ctx context.Context, project string, resources []declare.Resource) ([]resolve.Resource, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	store, err := c.running(ctx, project)
	if err != nil {
		return nil, err
	}

	out := make([]resolve.Resource, 0, len(resources))
	changed := c.service == nil
	for _, resource := range resources {
		name := bucketName(resource.Name)
		origins := append(slices.Clone(resource.Bucket.GetAllowedOrigins()), c.appOrigins()...)
		if provisioned, known := c.buckets[name]; !known || !slices.Equal(provisioned, origins) {
			if err := store.EnsureBucket(ctx, name, origins); err != nil {
				return nil, fmt.Errorf("bucket %q: %w", resource.Name, err)
			}
			c.buckets[name] = origins
			changed = true
		}
		bound, err := bind(resource, name)
		if err != nil {
			return nil, err
		}
		bound.Origin = fmt.Sprintf("rustfs %s @ %s", name, c.container.Addr)
		out = append(out, bound)
	}
	if changed {
		c.service = serve(*store, c.buckets)
	}
	return out, nil
}

var unsafeInBucketName = regexp.MustCompile(`[^a-z0-9]+`)

func bucketName(resource string) string {
	sum := sha256.Sum256([]byte(resource))
	readable := strings.Trim(unsafeInBucketName.ReplaceAllString(strings.ToLower(resource), "-"), "-")
	if len(readable) > 40 {
		readable = strings.Trim(readable[:40], "-")
	}
	return strings.Trim("dev-"+readable, "-") + "-" + hex.EncodeToString(sum[:4])
}

func bind(resource declare.Resource, name string) (resolve.Resource, error) {
	return resolve.Bound(resource.Type, &bindingsv1.Binding{
		Name:       resource.Name,
		Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: name}},
	})
}

func (c *Component) running(ctx context.Context, project string) (*s3store.Store, error) {
	if c.store != nil {
		return c.store, nil
	}
	secret, err := keptSecret(c.stateDir)
	if err != nil {
		return nil, err
	}
	if c.container == nil {
		engine, err := c.open(ctx)
		if err != nil {
			return nil, err
		}
		name := docker.Name(project, component)
		container, err := engine.Run(ctx, docker.Spec{
			Name:  name,
			Image: constants.ObjectStoreImage(),
			Env: []string{
				"RUSTFS_ACCESS_KEY=" + accessKeyID,
				"RUSTFS_SECRET_KEY=" + secret,
				"RUSTFS_ADDRESS=:" + strconv.Itoa(storePort),
				"RUSTFS_CONSOLE_ENABLE=false",
				"RUSTFS_REGION=" + storeRegion,
				"RUSTFS_VOLUMES=" + dataPath,
			},
			Port:       storePort,
			Volume:     name,
			VolumePath: dataPath,
			Labels:     docker.Labels(project, component),
		})
		if err != nil {
			return nil, err
		}
		c.container = &container
	}
	store := s3store.Store{
		Endpoint:        "http://" + c.container.Addr,
		Region:          storeRegion,
		AccessKeyID:     accessKeyID,
		SecretAccessKey: secret,
		PathStyle:       true,
	}
	if err := waitReady(ctx, store.Endpoint); err != nil {
		return nil, err
	}
	if err := store.EnsureBucket(ctx, constants.StoreSessionsBucket(), nil); err != nil {
		return nil, fmt.Errorf("keep upload sessions: %w", err)
	}
	c.store = &store
	return c.store, nil
}

func waitReady(ctx context.Context, endpoint string) error {
	err := docker.WaitReady(ctx, readyIn, func(ctx context.Context) error {
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, endpoint+healthPath, nil)
		if err != nil {
			return err
		}
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			return err
		}
		_ = resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return fmt.Errorf("the bucket store's readiness check answered %s", resp.Status)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("the bucket store never answered: %w", err)
	}
	return nil
}

func serve(store s3store.Store, buckets map[string][]string) *s3store.Service {
	presigner := store.Presigner()
	var allowed []string
	for _, origins := range buckets {
		allowed = append(allowed, origins...)
	}
	return s3store.New(s3store.Config{
		Objects:      store.Client(),
		Internal:     presigner,
		External:     func(context.Context) (s3store.PresignAPI, string) { return presigner, store.Endpoint },
		Callbacks:    callbacks{allowed: allowed},
		SweepUploads: true,
		Sessions:     constants.StoreSessionsBucket(),
		Granted:      slices.Sorted(maps.Keys(buckets)),
	})
}

func (c *Component) current() (*s3store.Service, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.service == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this app declares no bucket, so there is nowhere to upload to"))
	}
	return c.service, nil
}

func (c *Component) PresignUpload(ctx context.Context, req *bucketv1.PresignUploadRequest) (*bucketv1.PresignUploadResponse, error) {
	service, err := c.current()
	if err != nil {
		return nil, err
	}
	return service.PresignUpload(ctx, req)
}

func (c *Component) VerifyUploadSignature(ctx context.Context, req *bucketv1.VerifyUploadSignatureRequest) (*bucketv1.VerifyUploadSignatureResponse, error) {
	service, err := c.current()
	if err != nil {
		return nil, err
	}
	return service.VerifyUploadSignature(ctx, req)
}

func (c *Component) GetUploadStatus(ctx context.Context, req *bucketv1.GetUploadStatusRequest) (*bucketv1.GetUploadStatusResponse, error) {
	service, err := c.current()
	if err != nil {
		return nil, err
	}
	return service.GetUploadStatus(ctx, req)
}

func (c *Component) CompleteUpload(ctx context.Context, req *bucketv1.CompleteUploadRequest) (*bucketv1.CompleteUploadResponse, error) {
	service, err := c.current()
	if err != nil {
		return nil, err
	}
	return service.CompleteUpload(ctx, req)
}

func (c *Component) Head(ctx context.Context, req *bucketv1.HeadRequest) (*bucketv1.HeadResponse, error) {
	service, err := c.current()
	if err != nil {
		return nil, err
	}
	return service.Head(ctx, req)
}

func (c *Component) List(ctx context.Context, req *bucketv1.ListRequest) (*bucketv1.ListResponse, error) {
	service, err := c.current()
	if err != nil {
		return nil, err
	}
	return service.List(ctx, req)
}

func (c *Component) Delete(ctx context.Context, req *bucketv1.DeleteRequest) (*bucketv1.DeleteResponse, error) {
	service, err := c.current()
	if err != nil {
		return nil, err
	}
	return service.Delete(ctx, req)
}

func (c *Component) Copy(ctx context.Context, req *bucketv1.CopyRequest) (*bucketv1.CopyResponse, error) {
	service, err := c.current()
	if err != nil {
		return nil, err
	}
	return service.Copy(ctx, req)
}

func (c *Component) Sign(ctx context.Context, req *bucketv1.SignRequest) (*bucketv1.SignResponse, error) {
	service, err := c.current()
	if err != nil {
		return nil, err
	}
	return service.Sign(ctx, req)
}

func (c *Component) CreateMultipart(ctx context.Context, req *bucketv1.CreateMultipartRequest) (*bucketv1.CreateMultipartResponse, error) {
	service, err := c.current()
	if err != nil {
		return nil, err
	}
	return service.CreateMultipart(ctx, req)
}

func (c *Component) SignParts(ctx context.Context, req *bucketv1.SignPartsRequest) (*bucketv1.SignPartsResponse, error) {
	service, err := c.current()
	if err != nil {
		return nil, err
	}
	return service.SignParts(ctx, req)
}

func (c *Component) CompleteMultipart(ctx context.Context, req *bucketv1.CompleteMultipartRequest) (*bucketv1.CompleteMultipartResponse, error) {
	service, err := c.current()
	if err != nil {
		return nil, err
	}
	return service.CompleteMultipart(ctx, req)
}

func (c *Component) AbortMultipart(ctx context.Context, req *bucketv1.AbortMultipartRequest) (*bucketv1.AbortMultipartResponse, error) {
	service, err := c.current()
	if err != nil {
		return nil, err
	}
	return service.AbortMultipart(ctx, req)
}

func (c *Component) Routes(mux *http.ServeMux, guard func(http.Handler) http.Handler, options ...connect.HandlerOption) {
	path, handler := bucketv1connect.NewBucketServiceHandler(c, options...)
	mux.Handle(path, guard(handler))
}

func (c *Component) Close(ctx context.Context, stop bool) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	c.store = nil
	c.service = nil
	c.buckets = map[string][]string{}
	if c.container == nil {
		return nil
	}
	running := c.container
	c.container = nil
	if !stop {
		return nil
	}
	engine, err := c.open(ctx)
	if err != nil {
		return err
	}
	return engine.Stop(ctx, running.ID)
}
