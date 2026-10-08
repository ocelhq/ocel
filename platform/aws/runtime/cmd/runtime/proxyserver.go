package main

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"slices"

	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/sns"
	"github.com/aws/aws-sdk-go-v2/service/sqs"

	"google.golang.org/protobuf/encoding/protojson"

	"github.com/ocelhq/ocel/pkg/naming"
	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/platform/aws/provider/bucket"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/aws/runtime/realtime"
	"github.com/ocelhq/ocel/platform/aws/runtime/tasks"
	realtimeproxy "github.com/ocelhq/ocel/platform/realtime/proxy"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

const (
	stateTableEnvVar    = "OCEL_RUNTIME_STATE_TABLE"
	sessionPrefixEnvVar = "OCEL_RUNTIME_SESSION_PREFIX"
)

type proxyConfig struct {
	table         string
	sessionPrefix string
	queues        *queues.Topology
	worker        string
	workerURL     string
}

type proxy struct {
	env    []string
	errs   <-chan error
	engine *tasks.Engine
}

func proxyWanted(bindings []live.Binding) bool {
	for _, l := range bindings {
		if naming.Proxied(l.Type) {
			return true
		}
	}
	return false
}

func bindsAny(bindings []live.Binding, types ...bindingsv1.BindingType) bool {
	return slices.ContainsFunc(bindings, func(l live.Binding) bool { return slices.Contains(types, l.Type) })
}

func grantedBuckets(values s3store.Records) func() []string {
	return func() []string {
		var buckets []string
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
			if name := record.GetBucket().GetBucket(); name != "" && !slices.Contains(buckets, name) {
				buckets = append(buckets, name)
			}
		}
		return buckets
	}
}

func readQueueTopology(root string) (*queues.Topology, error) {
	data, err := os.ReadFile(filepath.Join(root, queues.FilePath))
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", queues.FilePath, err)
	}
	topology, err := queues.Parse(data)
	if err != nil {
		return nil, err
	}
	return &topology, nil
}

func serveProxy(ctx context.Context, values s3store.Records, cfg proxyConfig) (proxy, error) {
	bindings := values.Bindings()
	servesTasks := cfg.worker != "" || bindsAny(bindings, bindingsv1.BindingType_BINDING_TYPE_TASK, bindingsv1.BindingType_BINDING_TYPE_TOPIC)
	if !proxyWanted(bindings) && !servesTasks {
		return proxy{}, nil
	}
	servesBuckets := bindsAny(bindings, bindingsv1.BindingType_BINDING_TYPE_BUCKET)
	if servesBuckets && cfg.table == "" {
		return proxy{}, fmt.Errorf("%s is not set, so the sessions this deployment's buckets keep have nowhere to live", stateTableEnvVar)
	}
	if servesBuckets && cfg.sessionPrefix == "" {
		return proxy{}, fmt.Errorf("%s is not set, so this deployment's sessions would share a key space with every other deployment in the account", sessionPrefixEnvVar)
	}
	if servesTasks && cfg.queues == nil {
		return proxy{}, fmt.Errorf("this deployment binds a task or topic, and its code carries no %s naming the queues behind them", queues.FilePath)
	}

	aws, err := sdkconfig.Workload(ctx)
	if err != nil {
		return proxy{}, fmt.Errorf("load aws config: %w", err)
	}
	db := dynamodb.NewFromConfig(aws)
	var services bindingproxy.Services
	if servesBuckets {
		objects := s3.NewFromConfig(aws)
		services.Buckets = s3store.NewDispatch(bucket.New(bucket.Config{
			DDB:              db,
			Presigner:        s3.NewPresignClient(objects),
			Objects:          objects,
			Table:            cfg.table,
			SessionKeyPrefix: cfg.sessionPrefix,
			Granted:          grantedBuckets(values),
		}), values, s3store.HTTPPoster{})
	}
	if bindsAny(bindings, bindingsv1.BindingType_BINDING_TYPE_REALTIME) {
		services.Realtime = realtimeproxy.NewService(values, realtime.NewAppSyncTransport(realtime.Config{
			Client:      http.DefaultClient,
			Credentials: aws.Credentials,
			Retryer:     sdkconfig.WorkloadRetryer(),
			Region:      aws.Region,
		}))
	}
	var engine *tasks.Engine
	if servesTasks {
		engine = tasks.New(tasks.Config{
			Topology:  *cfg.queues,
			Table:     db,
			Queues:    sqs.NewFromConfig(aws),
			Topics:    sns.NewFromConfig(aws),
			Worker:    cfg.worker,
			WorkerURL: cfg.workerURL,
		})
		services.Tasks, services.Topics = engine.Tasks(), engine.Topics()
	}

	served, err := bindingproxy.Serve(services)
	if err != nil {
		return proxy{}, err
	}
	return proxy{env: served.Env, errs: served.Errs, engine: engine}, nil
}

func superviseProxy(served <-chan error) {
	if served == nil {
		return
	}
	err := <-served
	fmt.Fprintf(os.Stderr, "ocel: the proxy stopped serving this deployment's bindings: %v\n", err)
	os.Exit(1)
}
