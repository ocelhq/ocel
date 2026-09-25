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

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/envsource"
	"github.com/ocelhq/ocel/pkg/providerkit/values"
	"github.com/ocelhq/ocel/platform/aws/provider/envidentity"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
)

const (
	varsTableEnvVar = "OCEL_VARS_TABLE"
	varsKeyEnvVar   = "OCEL_VARS_KEY"
	classEnvVar     = "OCEL_INFRA_CLASS"

	requestWindow = 30 * time.Second
)

type poller interface {
	Poll(ctx context.Context) error
}

type invocation struct {
	syncer poller
	errs   io.Writer
}

func (i invocation) handle(ctx context.Context) error {
	if err := i.syncer.Poll(ctx); err != nil {
		fmt.Fprintf(i.errs, "ocel envsync: %s\n", err)
	}
	return nil
}

func main() {
	syncer, err := newSyncer(context.Background(), os.Getenv)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ocel envsync: %v\n", err)
		os.Exit(1)
	}
	lambda.Start(invocation{syncer: syncer, errs: os.Stderr}.handle)
}

func newSyncer(ctx context.Context, getenv func(string) string) (*envsource.Syncer, error) {
	table := getenv(varsTableEnvVar)
	if table == "" {
		return nil, fmt.Errorf("%s is not set, so there is no table to read registrations from or write values into", varsTableEnvVar)
	}
	key := getenv(varsKeyEnvVar)
	if key == "" {
		return nil, fmt.Errorf("%s is not set, so no value this syncer writes could be sealed", varsKeyEnvVar)
	}
	class := providerkit.Class(getenv(classEnvVar))
	switch class {
	case providerkit.ClassProduction, providerkit.ClassPreview:
	default:
		return nil, fmt.Errorf("%s is %q, want %s or %s", classEnvVar, class, providerkit.ClassProduction, providerkit.ClassPreview)
	}
	cfg, err := sdkconfig.Runtime(ctx)
	if err != nil {
		return nil, fmt.Errorf("load aws config: %w", err)
	}
	return &envsource.Syncer{
		Store: values.Store{
			Records: awsports.Records{Dynamo: dynamodb.NewFromConfig(cfg), Tables: awsports.Table(table)},
			Sealer:  awsports.Sealer{KMS: kms.NewFromConfig(cfg), Keys: awsports.Key(key)},
		},
		Class: class,
		Target: envsource.Target{
			Signer: envidentity.Signer{Config: cfg},
			Client: &http.Client{Timeout: requestWindow},
		},
	}, nil
}
