package postgres

import (
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const (
	pooledConnections   = 10
	reservedConnections = 3
)

type ConnectionBudget struct {
	app       string
	databases []string
	size      cloudSQLSize
	tier      string
	services  int
}

func ConnectionBudgetOf(spec provider.StackSpec, services int) ConnectionBudget {
	budget := ConnectionBudget{app: spec.App.App, size: cloudSQLSizeOf(spec.Ref.Tier), tier: string(spec.Ref.Tier), services: max(services, 1)}
	for _, binding := range spec.App.Values.Bindings {
		if binding.Type == provider.BindingPostgres {
			budget.databases = append(budget.databases, binding.Name)
		}
	}
	return budget
}

func (b ConnectionBudget) appConnections() int {
	return b.size.maxConnections - reservedConnections
}

func (b ConnectionBudget) Ceiling() int {
	if len(b.databases) == 0 {
		return 0
	}
	return max(1, b.appConnections()/pooledConnections/b.services)
}

func (b ConnectionBudget) InstancesFor(requested provider.Instances) (provider.Instances, error) {
	ceiling := b.Ceiling()
	if ceiling == 0 {
		return requested, nil
	}
	if asked := max(requested.Min, requested.Max); asked > ceiling {
		return provider.Instances{}, refusal.Refuse(refusal.CodeInvalid,
			"app %s asks for %d instances, and each opens up to %d connections to postgres %s, whose %s Cloud SQL instance %s takes %d from the app's %d Cloud Run services: "+
				"ask for at most %d instances",
			b.app, asked, pooledConnections, b.named(), b.tier, b.size.machineType, b.appConnections(), b.services, ceiling)
	}
	if requested.Max == 0 {
		requested.Max = ceiling
	}
	return requested, nil
}

func (b ConnectionBudget) WarnOversubscribed(progress progress.Log) {
	opened := b.services * pooledConnections
	if len(b.databases) == 0 || opened <= b.appConnections() {
		return
	}
	gcloud.EnsureProgress(progress).Warn(fmt.Sprintf(
		"app %s runs %d Cloud Run services bound to postgres %s, and each instance opens up to %d connections, so at one instance each they open %d, "+
			"more than the %d the %s tier's %s Cloud SQL instance takes from apps: a query waits or fails once they are used up. Serve the app from fewer services",
		b.app, b.services, b.named(), pooledConnections, opened, b.appConnections(), b.tier, b.size.machineType))
}

func (b ConnectionBudget) named() string {
	return strings.Join(b.databases, ", ")
}
