package bucket

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"net/http"
	"regexp"
	"slices"
	"strings"
	"sync"
	"time"

	connect "connectrpc.com/connect"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
	"github.com/ocelhq/ocel/cli/internal/resolve"
	bucketv1 "github.com/ocelhq/ocel/pkg/proto/app/bucket/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/bucket/v1/bucketv1connect"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	production "github.com/ocelhq/ocel/platform/aws/runtime/bucket"
)

const (
	component = "bucket"

	// TODO(#1203): this build verifies no presigned signature, so an upload's signed content type
	// and length are not enforced in dev; move the pin and unskip
	// TestAnUploadThatBreaksItsSignedConditionsIsRefused once ocelhq/floci verifies them.
	image = "ghcr.io/ocelhq/floci:2.0.1-ocel.2@sha256:3e541597f1aeaf99e0ff12aaaa5296da665c280a1c60bf9a3c7b751602defe9f"

	emulatorPort = 4566
	dataPath     = "/app/data"
	healthPath   = "/_localstack/health"
	readyIn      = 3 * time.Minute

	emulatorRegion = "us-east-1"
	emulatorKey    = "test"

	sessionTable  = "ocel-dev-upload-sessions"
	sessionPrefix = "dev#"
)

var _ bucketv1connect.BucketServiceHandler = (*Component)(nil)

type emulator struct {
	container docker.Container
	s3        *s3.Client
	ddb       *dynamodb.Client
	sqs       *sqs.Client
	service   *production.Service
	uploads   *uploads
}

type Component struct {
	open       docker.Opener
	appOrigins func() []string
	report     func(error)

	mu        sync.Mutex
	container *docker.Container
	emulator  *emulator
}

func New(open docker.Opener, appOrigins func() []string, report func(error)) *Component {
	if report == nil {
		report = func(error) {}
	}
	return &Component{open: open, appOrigins: appOrigins, report: report}
}

func (c *Component) Resolve(ctx context.Context, project string, resources []declare.Resource) ([]resolve.Resource, error) {
	c.mu.Lock()
	defer c.mu.Unlock()

	running, err := c.running(ctx, project)
	if err != nil {
		return nil, err
	}

	out := make([]resolve.Resource, 0, len(resources))
	for _, resource := range resources {
		name := bucketName(resource.Name)
		origins := append(slices.Clone(resource.Bucket.GetAllowedOrigins()), c.appOrigins()...)
		if err := running.provision(ctx, name, origins); err != nil {
			return nil, fmt.Errorf("bucket %q: %w", resource.Name, err)
		}
		bound, err := bind(resource, name)
		if err != nil {
			return nil, err
		}
		bound.Origin = fmt.Sprintf("floci s3://%s @ %s", name, running.container.Addr)
		out = append(out, bound)
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

func (c *Component) running(ctx context.Context, project string) (*emulator, error) {
	if c.emulator != nil {
		return c.emulator, nil
	}
	if c.container == nil {
		engine, err := c.open(ctx)
		if err != nil {
			return nil, err
		}
		name := docker.Name(project, component)
		container, err := engine.Run(ctx, docker.Spec{
			Name:       name,
			Image:      image,
			Env:        []string{"FLOCI_STORAGE_MODE=persistent"},
			Port:       emulatorPort,
			Volume:     name,
			VolumePath: dataPath,
			Labels:     docker.Labels(project, component),
		})
		if err != nil {
			return nil, err
		}
		c.container = &container
	}
	started, err := start(ctx, *c.container, c.report)
	if err != nil {
		return nil, err
	}
	c.emulator = started
	return started, nil
}

func start(ctx context.Context, container docker.Container, report func(error)) (*emulator, error) {
	endpoint := "http://" + container.Addr
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
			return fmt.Errorf("the emulator's health check answered %s", resp.Status)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("the bucket emulator never answered: %w", err)
	}

	cfg := aws.Config{
		Region:       emulatorRegion,
		BaseEndpoint: aws.String(endpoint),
		Credentials:  credentials.NewStaticCredentialsProvider(emulatorKey, emulatorKey, ""),
	}
	running := &emulator{
		container: container,
		s3:        s3.NewFromConfig(cfg, func(o *s3.Options) { o.UsePathStyle = true }),
		ddb:       dynamodb.NewFromConfig(cfg),
		sqs:       sqs.NewFromConfig(cfg),
	}
	if err := ensureSessionTable(ctx, running.ddb); err != nil {
		return nil, err
	}
	running.uploads, err = watchUploads(ctx, running.sqs, report)
	if err != nil {
		return nil, err
	}
	running.service = production.New(production.Config{
		DDB:              running.ddb,
		Presigner:        s3.NewPresignClient(running.s3),
		Objects:          running.s3,
		Table:            sessionTable,
		SessionKeyPrefix: sessionPrefix,
		Granted:          running.uploads.granted,
	})
	return running, nil
}

func (c *Component) service() (*production.Service, error) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.emulator == nil {
		return nil, connect.NewError(connect.CodeFailedPrecondition, errors.New("this app declares no bucket, so there is nowhere to upload to"))
	}
	return c.emulator.service, nil
}

func (c *Component) PresignUpload(ctx context.Context, req *bucketv1.PresignUploadRequest) (*bucketv1.PresignUploadResponse, error) {
	service, err := c.service()
	if err != nil {
		return nil, err
	}
	return service.PresignUpload(ctx, req)
}

func (c *Component) VerifyUploadSignature(ctx context.Context, req *bucketv1.VerifyUploadSignatureRequest) (*bucketv1.VerifyUploadSignatureResponse, error) {
	service, err := c.service()
	if err != nil {
		return nil, err
	}
	return service.VerifyUploadSignature(ctx, req)
}

func (c *Component) GetUploadStatus(ctx context.Context, req *bucketv1.GetUploadStatusRequest) (*bucketv1.GetUploadStatusResponse, error) {
	service, err := c.service()
	if err != nil {
		return nil, err
	}
	return service.GetUploadStatus(ctx, req)
}

func (c *Component) CompleteUpload(ctx context.Context, req *bucketv1.CompleteUploadRequest) (*bucketv1.CompleteUploadResponse, error) {
	service, err := c.service()
	if err != nil {
		return nil, err
	}
	return service.CompleteUpload(ctx, req)
}

func (c *Component) Head(ctx context.Context, req *bucketv1.HeadRequest) (*bucketv1.HeadResponse, error) {
	service, err := c.service()
	if err != nil {
		return nil, err
	}
	return service.Head(ctx, req)
}

func (c *Component) List(ctx context.Context, req *bucketv1.ListRequest) (*bucketv1.ListResponse, error) {
	service, err := c.service()
	if err != nil {
		return nil, err
	}
	return service.List(ctx, req)
}

func (c *Component) Delete(ctx context.Context, req *bucketv1.DeleteRequest) (*bucketv1.DeleteResponse, error) {
	service, err := c.service()
	if err != nil {
		return nil, err
	}
	return service.Delete(ctx, req)
}

func (c *Component) Copy(ctx context.Context, req *bucketv1.CopyRequest) (*bucketv1.CopyResponse, error) {
	service, err := c.service()
	if err != nil {
		return nil, err
	}
	return service.Copy(ctx, req)
}

func (c *Component) Sign(ctx context.Context, req *bucketv1.SignRequest) (*bucketv1.SignResponse, error) {
	service, err := c.service()
	if err != nil {
		return nil, err
	}
	return service.Sign(ctx, req)
}

func (c *Component) CreateMultipart(ctx context.Context, req *bucketv1.CreateMultipartRequest) (*bucketv1.CreateMultipartResponse, error) {
	service, err := c.service()
	if err != nil {
		return nil, err
	}
	return service.CreateMultipart(ctx, req)
}

func (c *Component) SignParts(ctx context.Context, req *bucketv1.SignPartsRequest) (*bucketv1.SignPartsResponse, error) {
	service, err := c.service()
	if err != nil {
		return nil, err
	}
	return service.SignParts(ctx, req)
}

func (c *Component) CompleteMultipart(ctx context.Context, req *bucketv1.CompleteMultipartRequest) (*bucketv1.CompleteMultipartResponse, error) {
	service, err := c.service()
	if err != nil {
		return nil, err
	}
	return service.CompleteMultipart(ctx, req)
}

func (c *Component) AbortMultipart(ctx context.Context, req *bucketv1.AbortMultipartRequest) (*bucketv1.AbortMultipartResponse, error) {
	service, err := c.service()
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

	if c.emulator != nil {
		c.emulator.uploads.stop()
		c.emulator = nil
	}
	if c.container == nil {
		return nil
	}
	held := c.container
	c.container = nil
	if !stop {
		return nil
	}
	engine, err := c.open(ctx)
	if err != nil {
		return err
	}
	return engine.Stop(ctx, held.ID)
}
