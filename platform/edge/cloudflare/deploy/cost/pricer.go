package cost

import (
	_ "embed"
	"sync"

	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pkg/costkit"
)

const (
	Vendor = "cloudflare"

	TypeAccountSubscription = "cloudflare_account_subscription"
	TypeWorkersScript       = "cloudflare_workers_script"
	TypeR2Bucket            = "cloudflare_r2_bucket"
)

//go:embed rates.json
var rates []byte

var Card = sync.OnceValues(func() (*costkit.Card, error) { return costkit.Load(rates) })

var Notes = []string{
	"Workers, Durable Objects and R2 carry one global price, so the estimate does not move with the region",
}

const (
	usageRequests     = "monthly_requests"
	usageCPUTime      = "cpu_time_ms"
	usageStorage      = "storage_gb"
	usageClassA       = "monthly_class_a_operations"
	usageClassB       = "monthly_class_b_operations"
	usageObjectCalls  = "monthly_durable_object_requests"
	usageObjectTime   = "durable_object_duration_ms"
	usageRowsRead     = "monthly_rows_read"
	usageRowsWritten  = "monthly_rows_written"
	usageObjectStored = "durable_object_storage_gb"

	durableObjectGB = 0.128
	durableObjects  = "durable_objects"
)

var (
	requestsBand   = costkit.Band{Light: 100_000, Moderate: 1_000_000, Heavy: 10_000_000}
	cpuBand        = costkit.Band{Light: 5, Moderate: 5, Heavy: 10}
	storageBand    = costkit.Band{Light: 1, Moderate: 10, Heavy: 100}
	classABand     = costkit.Band{Light: 10_000, Moderate: 100_000, Heavy: 1_000_000}
	classBBand     = costkit.Band{Light: 100_000, Moderate: 1_000_000, Heavy: 10_000_000}
	objectCalls    = costkit.Band{Light: 10_000, Moderate: 100_000, Heavy: 1_000_000}
	objectTimeBand = costkit.Band{Light: 50, Moderate: 50, Heavy: 100}
	rowsReadBand   = costkit.Band{Light: 100_000, Moderate: 1_000_000, Heavy: 10_000_000}
	rowsWriteBand  = costkit.Band{Light: 10_000, Moderate: 100_000, Heavy: 1_000_000}
	objectStorage  = costkit.Band{Light: 0.1, Moderate: 1, Heavy: 10}
	thousand       = decimal.NewFromInt(1000)
)

var Table = costkit.Table{
	TypeAccountSubscription: subscription,
	TypeWorkersScript:       workersScript,
	TypeR2Bucket:            r2Bucket,
}

func subscription(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Workers paid plan", Unit: "months", Rate: "cloudflare/workers/paid-plan", Quantity: decimal.NewFromInt(1)})
}

func workersScript(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Requests", Unit: "requests", Rate: "cloudflare/workers/requests", Quantity: r.Usage(usageRequests, requestsBand), UsageBased: true})
	r.Add(costkit.Component{Name: "CPU time", Unit: "CPU-milliseconds", Rate: "cloudflare/workers/cpu-time", Quantity: r.Usage(usageRequests, requestsBand).Mul(r.Usage(usageCPUTime, cpuBand)), UsageBased: true})
	if len(r.List(durableObjects)) == 0 {
		return
	}
	held := []string{durableObjects}
	r.Add(costkit.Component{Name: "Durable Object requests", Unit: "requests", Rate: "cloudflare/durable-objects/requests", Quantity: r.Usage(usageObjectCalls, objectCalls), UsageBased: true, Needs: held})
	seconds := r.Usage(usageObjectCalls, objectCalls).Mul(r.Usage(usageObjectTime, objectTimeBand)).Div(thousand)
	r.Add(costkit.Component{Name: "Durable Object duration", Unit: "GB-seconds", Rate: "cloudflare/durable-objects/duration", Quantity: seconds.Mul(decimal.NewFromFloat(durableObjectGB)), UsageBased: true, Needs: held})
	r.Add(costkit.Component{Name: "Rows read", Unit: "rows", Rate: "cloudflare/durable-objects/rows-read", Quantity: r.Usage(usageRowsRead, rowsReadBand), UsageBased: true, Needs: held})
	r.Add(costkit.Component{Name: "Rows written", Unit: "rows", Rate: "cloudflare/durable-objects/rows-written", Quantity: r.Usage(usageRowsWritten, rowsWriteBand), UsageBased: true, Needs: held})
	r.Add(costkit.Component{Name: "Stored data", Unit: "GB-month", Rate: "cloudflare/durable-objects/storage", Quantity: r.Usage(usageObjectStored, objectStorage), UsageBased: true, Needs: held})
}

func r2Bucket(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Storage", Unit: "GB-month", Rate: "cloudflare/r2/storage", Quantity: r.Usage(usageStorage, storageBand), UsageBased: true})
	r.Add(costkit.Component{Name: "Class A operations", Unit: "operations", Rate: "cloudflare/r2/class-a", Quantity: r.Usage(usageClassA, classABand), UsageBased: true})
	r.Add(costkit.Component{Name: "Class B operations", Unit: "operations", Rate: "cloudflare/r2/class-b", Quantity: r.Usage(usageClassB, classBBand), UsageBased: true})
}
