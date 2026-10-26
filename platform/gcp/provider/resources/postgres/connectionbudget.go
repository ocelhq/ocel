package postgres

import (
	"fmt"
	"strings"

	sqladmin "google.golang.org/api/sqladmin/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const (
	pooledConnections   = 10
	reservedConnections = 3
	maxConnectionsFlag  = "max_connections"
)

var sharedCoreConnections = map[string]int{"db-f1-micro": 25, "db-g1-small": 50}

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

func refuseFewerConnections(database string, tier environment.Tier, desired *sqladmin.DatabaseInstance) error {
	for _, flag := range desired.Settings.DatabaseFlags {
		if flag.Name == maxConnectionsFlag {
			return refusal.Refuse(refusal.CodeInvalid,
				"a transform sets postgres %s's %s flag, and ocel sizes an app's connection pools to its machine type's default: drop the flag", database, maxConnectionsFlag)
		}
	}
	size := cloudSQLSizeOf(tier)
	held, shared := sharedCoreConnections[desired.Settings.Tier]
	if !shared || held >= size.maxConnections {
		return nil
	}
	return refusal.Refuse(refusal.CodeInvalid,
		"a transform runs postgres %s on %s, which takes %d connections, and the %s tier sizes an app's connection pools to the %d of %s: patch a dedicated-core machine type",
		database, desired.Settings.Tier, held, tier, size.maxConnections, size.machineType)
}
