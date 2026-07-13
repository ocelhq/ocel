// Command ocelawsrt is the Ocel AWS runtime binary: the production
// implementation of buckets.v1.BucketService (mint presigned PUT targets,
// persist/verify/read upload sessions in DynamoDB). It ships alongside the
// provider binary and is later wrapped by the membrane, which supervises it and
// injects its address into the app env. Nothing in this slice launches it in a
// deployed environment; it is exercised by a direct-dial integration test.
//
// It reuses the provider's local-channel conventions: it binds a private Unix
// socket (loopback TCP fallback), prints the OCEL_READY readiness
// sentinel once bound, and verifies a per-session token on every RPC.
package main

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/cloud/aws/runtime"
	"github.com/ocelhq/ocel/cloud/aws/runtime/bucket"
	"github.com/ocelhq/ocel/pkg/channel"
)

// version is set at build time via -ldflags "-X main.version=...".
var version = "dev"

// Environment contract. The launcher (the membrane, later) sets these; the
// endpoint overrides exist for local integration testing against
// dynamodb-local / MinIO.
const (
	sessionTokenEnvVar = channel.SessionTokenEnvVar
	tableEnvVar        = "OCEL_RUNTIME_SESSION_TABLE"
	bucketEnvVar       = "OCEL_RUNTIME_BUCKET"
	regionEnvVar       = "OCEL_RUNTIME_REGION"
	ddbEndpointEnvVar  = "OCEL_RUNTIME_DDB_ENDPOINT"
	s3EndpointEnvVar   = "OCEL_RUNTIME_S3_ENDPOINT"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintln(os.Stderr, "ocel aws runtime:", err)
		os.Exit(1)
	}
}

func run() error {
	token := os.Getenv(sessionTokenEnvVar)
	if token == "" {
		return fmt.Errorf("%s must be set by the launching process", sessionTokenEnvVar)
	}
	table := os.Getenv(tableEnvVar)
	if table == "" {
		return fmt.Errorf("%s must be set", tableEnvVar)
	}
	bucketName := os.Getenv(bucketEnvVar)
	if bucketName == "" {
		return fmt.Errorf("%s must be set", bucketEnvVar)
	}

	ctx := context.Background()
	svc, err := buildService(ctx, table, bucketName)
	if err != nil {
		return err
	}

	ln, addr, err := listen()
	if err != nil {
		return fmt.Errorf("bind runtime listener: %w", err)
	}
	defer ln.Close()

	fmt.Fprintf(os.Stderr, "ocel aws runtime %s: bound %s\n", version, addr)

	httpSrv := &http.Server{Handler: runtime.NewMux(token, svc)}

	sigCtx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	serveErr := make(chan error, 1)
	go func() { serveErr <- httpSrv.Serve(ln) }()

	// The listener is bound; announce readiness so a launcher can dial in.
	fmt.Println(channel.FormatReadinessLine(addr))

	select {
	case <-sigCtx.Done():
		return httpSrv.Close()
	case err := <-serveErr:
		if err != nil && err != http.ErrServerClosed {
			return err
		}
		return nil
	}
}

func buildService(ctx context.Context, table, bucketName string) (*bucket.Service, error) {
	optFns := []func(*awsconfig.LoadOptions) error{}
	if region := os.Getenv(regionEnvVar); region != "" {
		optFns = append(optFns, awsconfig.WithRegion(region))
	}
	cfg, err := awsconfig.LoadDefaultConfig(ctx, optFns...)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}

	s3Endpoint := os.Getenv(s3EndpointEnvVar)
	s3Client := s3.NewFromConfig(cfg, func(o *s3.Options) {
		if s3Endpoint != "" {
			o.BaseEndpoint = &s3Endpoint
			// A custom endpoint (MinIO / dynamodb-local rig) is path-style.
			o.UsePathStyle = true
		}
	})

	ddbEndpoint := os.Getenv(ddbEndpointEnvVar)
	ddbClient := dynamodb.NewFromConfig(cfg, func(o *dynamodb.Options) {
		if ddbEndpoint != "" {
			o.BaseEndpoint = &ddbEndpoint
		}
	})

	return bucket.New(bucket.Config{
		DDB:       ddbClient,
		Presigner: s3.NewPresignClient(s3Client),
		Table:     table,
		Bucket:    bucketName,
	}), nil
}
