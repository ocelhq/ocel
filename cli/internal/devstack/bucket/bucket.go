package bucket

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
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
	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/cli/internal/declare"
	"github.com/ocelhq/ocel/cli/internal/devstack/docker"
	"github.com/ocelhq/ocel/cli/internal/resolve"
	blobv1 "github.com/ocelhq/ocel/pkg/proto/app/blob/v1"
	"github.com/ocelhq/ocel/pkg/proto/app/blob/v1/blobv1connect"
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

var _ blobv1connect.BucketServiceHandler = (*Component)(nil)

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
	appOrigins []string
	report     func(error)

	mu       sync.Mutex
	engine   docker.Engine
	emulator *emulator
}

func New(open docker.Opener, appOrigins []string, report func(error)) *Component {
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
		origins := append(slices.Clone(resource.Bucket.GetAllowedOrigins()), c.appOrigins...)
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
	key, err := resolve.EnvName(resource.Type, resource.Name)
	if err != nil {
		return resolve.Resource{}, err
	}
	value, err := protojson.Marshal(&bindingsv1.Binding{
		Name:       resource.Name,
		Properties: &bindingsv1.Binding_Bucket{Bucket: &bindingsv1.BucketProperties{Bucket: name}},
	})
	if err != nil {
		return resolve.Resource{}, err
	}
	var stable bytes.Buffer
	if err := json.Compact(&stable, value); err != nil {
		return resolve.Resource{}, err
	}
	return resolve.Resource{Name: resource.Name, Type: resource.Type, Env: map[string]string{key: stable.String()}}, nil
}

func (c *Component) running(ctx context.Context, project string) (*emulator, error) {
	if c.emulator != nil {
		return c.emulator, nil
	}
	if c.engine == nil {
		engine, err := c.open(ctx)
		if err != nil {
			return nil, err
		}
		c.engine = engine
	}

	name := docker.Name(project, component)
	container, err := c.engine.Run(ctx, docker.Spec{
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
	started, err := start(ctx, container, c.report)
	if err != nil {
		_ = c.engine.Stop(ctx, container.ID)
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
	running.service = production.New(production.Config{
		DDB:              running.ddb,
		Presigner:        s3.NewPresignClient(running.s3),
		Table:            sessionTable,
		SessionKeyPrefix: sessionPrefix,
	})
	running.uploads, err = watchUploads(ctx, running.sqs, report)
	if err != nil {
		return nil, err
	}
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

func (c *Component) PresignUpload(ctx context.Context, req *blobv1.PresignUploadRequest) (*blobv1.PresignUploadResponse, error) {
	service, err := c.service()
	if err != nil {
		return nil, err
	}
	return service.PresignUpload(ctx, req)
}

func (c *Component) VerifyUploadSignature(ctx context.Context, req *blobv1.VerifyUploadSignatureRequest) (*blobv1.VerifyUploadSignatureResponse, error) {
	service, err := c.service()
	if err != nil {
		return nil, err
	}
	return service.VerifyUploadSignature(ctx, req)
}

func (c *Component) GetUploadStatus(ctx context.Context, req *blobv1.GetUploadStatusRequest) (*blobv1.GetUploadStatusResponse, error) {
	service, err := c.service()
	if err != nil {
		return nil, err
	}
	return service.GetUploadStatus(ctx, req)
}

func (c *Component) Routes(mux *http.ServeMux, guard func(http.Handler) http.Handler, options ...connect.HandlerOption) {
	path, handler := blobv1connect.NewBucketServiceHandler(c, options...)
	mux.Handle(path, guard(handler))
}

func (c *Component) Close(ctx context.Context) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	if c.emulator == nil {
		return nil
	}
	c.emulator.uploads.stop()
	err := c.engine.Stop(ctx, c.emulator.container.ID)
	c.emulator = nil
	return err
}
