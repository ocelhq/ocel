package main

import (
	"context"
	"fmt"
	"os"
	"slices"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtimekit/live"
	"github.com/ocelhq/ocel/pkg/runtimekit/proxy"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/aws/runtime/bucket"
	s3store "github.com/ocelhq/ocel/platform/s3"
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

type bindingValues = s3store.Records

func grantedBuckets(values bindingValues) func() []string {
	return func() []string {
		var held []string
		for _, l := range values.Bindings() {
			if l.Type != bindingsv1.BindingType_BINDING_TYPE_BUCKET {
				continue
			}
			record := &bindingsv1.Binding{}
			if err := protojson.Unmarshal([]byte(values.Value(l.Key)), record); err != nil {
				continue
			}
			if s3store.Endpointed(record.GetBucket()) {
				continue
			}
			if name := record.GetBucket().GetBucket(); name != "" && !slices.Contains(held, name) {
				held = append(held, name)
			}
		}
		return held
	}
}

func serveProxy(ctx context.Context, values bindingValues, table, sessionPrefix string) ([]string, <-chan error, error) {
	bindings := values.Bindings()
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
	objects := s3.NewFromConfig(cfg)
	svc := bucket.New(bucket.Config{
		DDB:              dynamodb.NewFromConfig(cfg),
		Presigner:        s3.NewPresignClient(objects),
		Objects:          objects,
		Table:            table,
		SessionKeyPrefix: sessionPrefix,
		Granted:          grantedBuckets(values),
	})

	served, err := proxy.Serve(s3store.RouteRecords(svc, values, s3store.HTTPPoster{}))
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
