package main

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"time"

	"github.com/aws/aws-lambda-go/lambda"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/kms"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/envsource"
	"github.com/ocelhq/ocel/pkg/envvars"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
)

const (
	requestTimeout = 30 * time.Second
)

type invocation struct {
	copyScheduled func(context.Context) error
	log           io.Writer
}

func (i invocation) handle(ctx context.Context) error {
	if err := i.copyScheduled(ctx); err != nil {
		fmt.Fprintf(i.log, "ocel envsourcesync: %s\n", err)
	}
	return nil
}

func main() {
	sync, err := newSync(context.Background(), os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel envsourcesync: %v\n", err)
		os.Exit(1)
	}
	lambda.Start(invocation{copyScheduled: sync.CopyScheduled, log: os.Stderr}.handle)
}

func newSync(ctx context.Context, getenv func(string) string) (*envsource.Sync, error) {
	table := getenv(awsports.VarsTableEnvVar)
	if table == "" {
		return nil, fmt.Errorf("%s is not set, so there is no table to read registrations from or write values into", awsports.VarsTableEnvVar)
	}
	key := getenv(awsports.VarsKeyEnvVar)
	if key == "" {
		return nil, fmt.Errorf("%s is not set, so no value this sync writes could be encrypted", awsports.VarsKeyEnvVar)
	}
	class := edge.Class(getenv(awsports.ClassEnvVar))
	switch class {
	case edge.ClassProduction, edge.ClassPreview:
	default:
		return nil, fmt.Errorf("%s is %q, want %s or %s", awsports.ClassEnvVar, class, edge.ClassProduction, edge.ClassPreview)
	}
	cfg, err := sdkconfig.Workload(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return &envsource.Sync{
		Store: envvars.Store{
			Records: awsports.Records{Dynamo: dynamodb.NewFromConfig(cfg), Tables: awsports.Table(table)},
			Cipher:  awsports.Cipher{KMS: kms.NewFromConfig(cfg), Keys: awsports.Key(key)},
		},
		Class: class,
		Login: envsource.Login{
			ProveIdentity: awsports.CallerIdentity{Config: cfg}.Prove,
			Client:        &http.Client{Timeout: requestTimeout},
		},
	}, nil
}
