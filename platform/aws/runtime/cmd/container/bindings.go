package main

import (
	"context"
	"fmt"
	"slices"

	bindingsv1 "github.com/ocelhq/ocel/pkg/proto/common/bindings/v1"
	"github.com/ocelhq/ocel/pkg/runtime/bindingproxy"
	"github.com/ocelhq/ocel/pkg/runtime/live"
	"github.com/ocelhq/ocel/platform/aws/provider/queues"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/aws/runtime/tasks"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

func bindsTasks(bindings []live.Binding) bool {
	return slices.ContainsFunc(bindings, func(l live.Binding) bool {
		return l.Type == bindingsv1.BindingType_BINDING_TYPE_TASK || l.Type == bindingsv1.BindingType_BINDING_TYPE_TOPIC
	})
}

func serveBindings(ctx context.Context, records s3store.Records, app, topology string) (bindingproxy.Served, error) {
	var services bindingproxy.Services
	services.Buckets = s3store.NewBoundDispatch(records, app)
	sends := bindsTasks(records.Bindings())
	if sends {
		if topology == "" {
			return bindingproxy.Served{}, fmt.Errorf("this deployment binds a task or topic, and %s names no queues behind them", queues.EnvVar)
		}
		parsed, err := queues.Parse([]byte(topology))
		if err != nil {
			return bindingproxy.Served{}, fmt.Errorf("read %s: %w", queues.EnvVar, err)
		}
		aws, err := sdkconfig.Workload(ctx)
		if err != nil {
			return bindingproxy.Served{}, fmt.Errorf("load aws config: %w", err)
		}
		engine := tasks.NewAWS(aws, parsed, "", "")
		services.Tasks, services.Topics = engine.Tasks(), engine.Topics()
	}
	if services.Buckets == nil && !sends {
		return bindingproxy.Served{}, nil
	}
	return bindingproxy.Serve(services)
}
