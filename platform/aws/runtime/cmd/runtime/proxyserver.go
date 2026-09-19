package main

import (
	"context"
	"fmt"
	"os"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	"github.com/ocelhq/ocel/pkg/runtimekit/proxy"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/aws/runtime/bucket"
)

const (
	stateTableEnvVar    = "OCEL_RUNTIME_STATE_TABLE"
	sessionPrefixEnvVar = "OCEL_RUNTIME_SESSION_PREFIX"
)

func proxyWanted(bindings []live.Binding) bool {
	for _, l := range bindings {
		if naming.Proxied(l.Type) {
			return true
		}
	}
	return false
}

func serveProxy(ctx context.Context, bindings []live.Binding, table, sessionPrefix string) ([]string, <-chan error, error) {
	if !proxyWanted(bindings) {
		return nil, nil, nil
	}
	if table == "" {
		return nil, nil, fmt.Errorf("%s is not set, so the sessions this deployment's buckets keep have nowhere to live", stateTableEnvVar)
	}
	if sessionPrefix == "" {
		return nil, nil, fmt.Errorf("%s is not set, so this deployment's sessions would share a key space with every other deployment in the account", sessionPrefixEnvVar)
	}

	cfg, err := sdkconfig.Runtime(ctx)
	if err != nil {
		return nil, nil, fmt.Errorf("load aws config: %w", err)
	}
	svc := bucket.New(bucket.Config{
		DDB:              dynamodb.NewFromConfig(cfg),
		Presigner:        s3.NewPresignClient(s3.NewFromConfig(cfg)),
		Table:            table,
		SessionKeyPrefix: sessionPrefix,
	})

	served, err := proxy.Serve(svc)
	if err != nil {
		return nil, nil, err
	}
	return served.Env, served.Errs, nil
}

func superviseProxy(served <-chan error) {
	if served == nil {
		return
	}
	err := <-served
	fmt.Fprintf(os.Stderr, "ocel: the proxy stopped serving this deployment's bindings: %v\n", err)
	os.Exit(1)
}
