package cloudflare

import (
	"slices"
	"strconv"

	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pkg/pricing"
	"github.com/ocelhq/ocel/pkg/pricing/ingest"
)

const (
	vendor = "cloudflare"

	serviceWorkers        = "workers"
	serviceDurableObjects = "durable-objects"

	usageRequests     = "monthly_requests"
	usageCPUTime      = "cpu_time_ms"
	usageObjectCalls  = "monthly_durable_object_requests"
	usageObjectTime   = "durable_object_duration_ms"
	usageRowsRead     = "monthly_rows_read"
	usageRowsWritten  = "monthly_rows_written"
	usageObjectStored = "durable_object_storage_gb"

	durableObjectGB = 0.128
	migrations      = "migrations"
	bindings        = "bindings"

	durableObjectBinding = "durable_object_namespace"
	staticCatalog        = "static/cloudflare.json"
)

var (
	requestsBand   = pricing.Band{Light: 100_000, Moderate: 1_000_000, Heavy: 10_000_000}
	cpuBand        = pricing.Band{Light: 5, Moderate: 5, Heavy: 10}
	objectCalls    = pricing.Band{Light: 10_000, Moderate: 100_000, Heavy: 1_000_000}
	objectTimeBand = pricing.Band{Light: 50, Moderate: 50, Heavy: 100}
	rowsReadBand   = pricing.Band{Light: 100_000, Moderate: 1_000_000, Heavy: 10_000_000}
	rowsWriteBand  = pricing.Band{Light: 10_000, Moderate: 100_000, Heavy: 1_000_000}
	objectStorage  = pricing.Band{Light: 0.1, Moderate: 1, Heavy: 10}
	thousand       = decimal.NewFromInt(1000)
)

var Rules = pricing.Rules{
	Table: pricing.Table{
		"cloudflare_account_subscription": subscription,
		"cloudflare_workers_script":       workersScript,
	},
	Ingest: ingest.Vendor{
		Vendor: vendor,
		Source: ingest.SourceStatic,
		Services: []ingest.Service{
			{Service: serviceWorkers, File: staticCatalog},
			{Service: serviceDurableObjects, File: staticCatalog},
		},
	},
}

func paid(service, metric string) pricing.Query {
	return pricing.Query{Vendor: vendor, Service: service, Attributes: map[string]string{"metric": metric, "plan": "paid"}}
}

func subscription(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Workers paid plan", Unit: "months", Price: paid(serviceWorkers, "subscription"), Quantity: decimal.NewFromInt(1)})
}

func workersScript(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Requests", Unit: "requests", Price: paid(serviceWorkers, "requests"), Allowance: pricing.AllowanceAccount,
		Quantity: r.Usage(usageRequests, requestsBand), UsageBased: true})
	r.Add(pricing.Component{Name: "CPU time", Unit: "CPU-milliseconds", Price: paid(serviceWorkers, "cpu-time"), Allowance: pricing.AllowanceAccount,
		Quantity: r.Usage(usageRequests, requestsBand).Mul(r.Usage(usageCPUTime, cpuBand)), UsageBased: true})
	var hidden []string
	defines := func() bool {
		if definesClasses(r) {
			return true
		}
		binds, unknown := bindsOwnClass(r)
		hidden = unknown
		return binds
	}
	if !r.When(defines) {
		return
	}
	needs := append([]string{migrations}, hidden...)
	r.Add(pricing.Component{Name: "Durable Object requests", Unit: "requests", Price: paid(serviceDurableObjects, "requests"), Allowance: pricing.AllowanceAccount,
		Quantity: r.Usage(usageObjectCalls, objectCalls), UsageBased: true, Needs: needs})
	seconds := r.Usage(usageObjectCalls, objectCalls).Mul(r.Usage(usageObjectTime, objectTimeBand)).Div(thousand)
	r.Add(pricing.Component{Name: "Durable Object duration", Unit: "GB-seconds", Price: paid(serviceDurableObjects, "duration"), Allowance: pricing.AllowanceAccount,
		Quantity: seconds.Mul(decimal.NewFromFloat(durableObjectGB)), UsageBased: true, Needs: needs})
	r.Add(pricing.Component{Name: "Rows read", Unit: "rows", Price: paid(serviceDurableObjects, "rows-read"), Allowance: pricing.AllowanceAccount,
		Quantity: r.Usage(usageRowsRead, rowsReadBand), UsageBased: true, Needs: needs})
	r.Add(pricing.Component{Name: "Rows written", Unit: "rows", Price: paid(serviceDurableObjects, "rows-written"), Allowance: pricing.AllowanceAccount,
		Quantity: r.Usage(usageRowsWritten, rowsWriteBand), UsageBased: true, Needs: needs})
	r.Add(pricing.Component{Name: "Stored data", Unit: "GB-month", Price: paid(serviceDurableObjects, "storage"), Allowance: pricing.AllowanceAccount,
		Quantity: r.Usage(usageObjectStored, objectStorage), UsageBased: true, Needs: needs})
}

var (
	classFields      = []string{"new_classes", "new_sqlite_classes"}
	movedClassFields = []string{"renamed_classes", "transferred_classes"}
)

func definesClasses(r *pricing.Subject) bool {
	for _, field := range classFields {
		if path := migrations + "." + field; r.Has(path) && len(r.List(path)) > 0 {
			return true
		}
	}
	for _, field := range movedClassFields {
		if path := migrations + "." + field; r.Has(path) && movesIn(r.List(path)) {
			return true
		}
	}
	if path := migrations + ".steps"; r.Has(path) {
		for _, step := range r.List(path) {
			if entry, _ := step.(map[string]any); stepDefines(entry) {
				return true
			}
		}
	}
	return false
}

func stepDefines(step map[string]any) bool {
	for _, field := range classFields {
		if classes, _ := step[field].([]any); len(classes) > 0 {
			return true
		}
	}
	for _, field := range movedClassFields {
		if moves, _ := step[field].([]any); movesIn(moves) {
			return true
		}
	}
	return false
}

func movesIn(moves []any) bool {
	for _, move := range moves {
		entry, _ := move.(map[string]any)
		if to, _ := entry["to"].(string); to != "" {
			return true
		}
	}
	return false
}

func bindsOwnClass(r *pricing.Subject) (bool, []string) {
	var listed int
	var hidden []string
	switch held := r.Resource.GetProperties().AsMap()[bindings].(type) {
	case []any:
		listed = len(held)
	case map[string]any:
		listed = 1
	default:
		if slices.Contains(r.Resource.GetUnknown(), bindings) {
			hidden = append(hidden, bindings)
		}
	}
	for i := range listed {
		at := bindings + "." + strconv.Itoa(i) + "."
		switch {
		case r.Unknown(at + "type"):
			hidden = append(hidden, at+"type")
		case r.StringOr(at+"type", "") != durableObjectBinding:
		case r.Unknown(at + "script_name"):
			hidden = append(hidden, at+"script_name")
		case r.StringOr(at+"script_name", "") == "":
			return true, nil
		}
	}
	for _, path := range hidden {
		r.Has(path)
	}
	return false, hidden
}
