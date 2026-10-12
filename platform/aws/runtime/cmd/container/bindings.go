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
	"github.com/ocelhq/ocel/platform/aws/runtime/realtime"
	"github.com/ocelhq/ocel/platform/aws/runtime/tasks"
	s3store "github.com/ocelhq/ocel/platform/s3"
)

func bindsAny(bindings []live.Binding, types ...bindingsv1.BindingType) bool {
	return slices.ContainsFunc(bindings, func(l live.Binding) bool { return slices.Contains(types, l.Type) })
}

func serveBindings(ctx context.Context, records s3store.Records, app, topology string) (bindingproxy.Served, error) {
	var services bindingproxy.Services
	services.Buckets = s3store.NewBoundDispatch(records, app)
	bindings := records.Bindings()
	sends := bindsAny(bindings, bindingsv1.BindingType_BINDING_TYPE_TASK, bindingsv1.BindingType_BINDING_TYPE_TOPIC)
	publishes := bindsAny(bindings, bindingsv1.BindingType_BINDING_TYPE_REALTIME)
	if sends || publishes {
		aws, err := sdkconfig.Workload(ctx)
		if err != nil {
			return bindingproxy.Served{}, fmt.Errorf("load aws config: %w", err)
		}
		if sends {
			if topology == "" {
				return bindingproxy.Served{}, fmt.Errorf("this deployment binds a task or topic, and %s names no queues behind them", queues.EnvVar)
			}
			parsed, err := queues.Parse([]byte(topology))
			if err != nil {
				return bindingproxy.Served{}, fmt.Errorf("read %s: %w", queues.EnvVar, err)
			}
			engine := tasks.NewAWS(aws, parsed, "", "")
			services.Tasks, services.Topics = engine.Tasks(), engine.Topics()
		}
		if publishes {
			services.Realtime = realtime.NewAppSyncService(records, aws)
		}
	}
	if services.Empty() {
		return bindingproxy.Served{}, nil
	}
	return bindingproxy.Serve(services)
}
