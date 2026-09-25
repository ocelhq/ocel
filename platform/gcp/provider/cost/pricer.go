package cost

import (
	_ "embed"
	"sync"

	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pkg/costkit"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

//go:embed rates.json
var rates []byte

var card = sync.OnceValues(func() (*costkit.Card, error) { return costkit.Load(rates) })

const (
	usageRequests        = "monthly_requests"
	usageRequestDuration = "average_request_duration_ms"
	usageInstanceHours   = "instance_hrs"
	usageStorage         = "storage_gb"
	usageClassA          = "monthly_class_a_operations"
	usageClassB          = "monthly_class_b_operations"
	usageReads           = "monthly_document_reads"
	usageWrites          = "monthly_document_writes"
	usageAccess          = "monthly_access_operations"
	usageKeyOperations   = "monthly_key_operations"
	usageDataProcessed   = "monthly_ingress_data_processed_gb"
	usageDataOut         = "monthly_data_transfer_to_internet_gb"
	usageVersions        = "active_secret_versions"

	ingressEverywhere = "INGRESS_TRAFFIC_ALL"
	cpuIdle           = "template.containers.0.resources.cpu_idle"
	mebibytesPerGiB   = 1024
	secondsPerHour    = 3600
)

var (
	requestsBand = costkit.Band{Light: 100_000, Moderate: 1_000_000, Heavy: 10_000_000}
	durationBand = costkit.Band{Light: 100, Moderate: 200, Heavy: 500}
	storageBand  = costkit.Band{Light: 1, Moderate: 10, Heavy: 100}
	classABand   = costkit.Band{Light: 1_000, Moderate: 10_000, Heavy: 100_000}
	classBBand   = costkit.Band{Light: 10_000, Moderate: 100_000, Heavy: 1_000_000}
	readsBand    = costkit.Band{Light: 100_000, Moderate: 1_000_000, Heavy: 10_000_000}
	writesBand   = costkit.Band{Light: 10_000, Moderate: 100_000, Heavy: 1_000_000}
	recordsBand  = costkit.Band{Light: 0.1, Moderate: 1, Heavy: 10}
	accessBand   = costkit.Band{Light: 1_000, Moderate: 10_000, Heavy: 100_000}
	egressBand   = costkit.Band{Light: 1, Moderate: 10, Heavy: 100}
	imagesBand   = costkit.Band{Light: 1, Moderate: 5, Heavy: 50}
	versionsBand = costkit.Band{Light: 1, Moderate: 1, Heavy: 3}
	thousand     = decimal.NewFromInt(1000)
)

var table = costkit.Table{
	"google_cloud_run_v2_service":                      cloudRunService,
	"google_firestore_database":                        firestoreDatabase,
	"google_storage_bucket":                            storageBucket,
	"google_kms_key_ring":                              free,
	"google_kms_crypto_key":                            cryptoKey,
	"google_service_account":                           free,
	"google_artifact_registry_repository":              artifactRepository,
	"google_secret_manager_secret":                     secret,
	"google_compute_global_address":                    free,
	"google_compute_backend_service":                   backendService,
	"google_certificate_manager_certificate_map":       free,
	"google_certificate_manager_certificate_map_entry": free,
	"google_compute_url_map":                           free,
	"google_compute_target_https_proxy":                free,
	"google_compute_global_forwarding_rule":            forwardingRule,
	"google_compute_region_network_endpoint_group":     free,
}

func Price(req *costv1.PriceRequest) (*costv1.Estimate, error) {
	held, err := card()
	if err != nil {
		return nil, err
	}
	merged, pricing, err := costkit.Priced(held, table)
	if err != nil {
		return nil, err
	}
	estimate, err := costkit.Estimate(merged, pricing, req)
	if err != nil {
		return nil, err
	}
	estimate.Notes = append(estimate.Notes,
		"list prices for the regions the card names and for global SKUs; a resource elsewhere is left unpriced unless a note above says otherwise",
		"the free tier Cloud Run and Cloud Storage apply as a billing discount is not applied; a free first tier the catalog publishes is spent once per account across every resource sharing it",
		"the forwarding rule is priced as the project's first five, which share one hourly charge",
	)
	return estimate, nil
}

func free(r *costkit.Subject) { r.Free() }

func cloudRunService(r *costkit.Subject) {
	cpu := func() decimal.Decimal { return r.Number("template.containers.0.resources.limits.cpu") }
	memoryGiB := func() decimal.Decimal {
		return quantityMiB(r.String("template.containers.0.resources.limits.memory")).Div(decimal.NewFromInt(mebibytesPerGiB))
	}
	billing := []string{cpuIdle}
	if !r.Bool(cpuIdle) {
		seconds := func() decimal.Decimal {
			warm, _ := r.Number("template.scaling.min_instance_count").Mul(costkit.MonthlyHours).Float64()
			return r.Usage(usageInstanceHours, costkit.Band{Light: warm, Moderate: warm, Heavy: warm}).Mul(decimal.NewFromInt(secondsPerHour))
		}
		r.Add(costkit.Component{Name: "CPU, always allocated", Unit: "vCPU-seconds", Rate: "gcp/run/cpu-always", Quantity: seconds().Mul(cpu()), Needs: billing})
		r.Add(costkit.Component{Name: "Memory, always allocated", Unit: "GiB-seconds", Rate: "gcp/run/memory-always", Quantity: seconds().Mul(memoryGiB()), Needs: billing})
	} else {
		seconds := func() decimal.Decimal {
			return r.Usage(usageRequests, requestsBand).Mul(r.Usage(usageRequestDuration, durationBand)).Div(thousand)
		}
		r.Add(costkit.Component{Name: "Requests", Unit: "requests", Rate: "gcp/run/requests", Quantity: r.Usage(usageRequests, requestsBand), UsageBased: true, Needs: billing})
		r.Add(costkit.Component{Name: "CPU during requests", Unit: "vCPU-seconds", Rate: "gcp/run/cpu-active", UsageBased: true,
			Quantity: seconds().Mul(cpu()), Needs: billing})
		r.Add(costkit.Component{Name: "Memory during requests", Unit: "GiB-seconds", Rate: "gcp/run/memory-active", UsageBased: true,
			Quantity: seconds().Mul(memoryGiB()), Needs: billing})
	}
	if r.String("ingress") == ingressEverywhere {
		r.Add(costkit.Component{Name: "Data transfer out to internet", Unit: "GiB", Rate: "gcp/network/premium-egress", Quantity: r.Usage(usageDataOut, egressBand), UsageBased: true})
	}
}

func quantityMiB(limit string) decimal.Decimal {
	if len(limit) > 2 && limit[len(limit)-2:] == "Mi" {
		if parsed, err := decimal.NewFromString(limit[:len(limit)-2]); err == nil {
			return parsed
		}
	}
	if len(limit) > 2 && limit[len(limit)-2:] == "Gi" {
		if parsed, err := decimal.NewFromString(limit[:len(limit)-2]); err == nil {
			return parsed.Mul(decimal.NewFromInt(mebibytesPerGiB))
		}
	}
	return decimal.Zero
}

func firestoreDatabase(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Document reads", Unit: "reads", Rate: "gcp/firestore/reads", Quantity: r.Usage(usageReads, readsBand), UsageBased: true})
	r.Add(costkit.Component{Name: "Document writes", Unit: "writes", Rate: "gcp/firestore/writes", Quantity: r.Usage(usageWrites, writesBand), UsageBased: true})
	r.Add(costkit.Component{Name: "Storage", Unit: "GiB-month", Rate: "gcp/firestore/storage", Quantity: r.Usage(usageStorage, recordsBand), UsageBased: true})
}

func storageBucket(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Storage", Unit: "GiB-month", Rate: "gcp/storage/standard", Quantity: r.Usage(usageStorage, storageBand), UsageBased: true})
	r.Add(costkit.Component{Name: "Class A operations", Unit: "operations", Rate: "gcp/storage/class-a", Quantity: r.Usage(usageClassA, classABand), UsageBased: true})
	r.Add(costkit.Component{Name: "Class B operations", Unit: "operations", Rate: "gcp/storage/class-b", Quantity: r.Usage(usageClassB, classBBand), UsageBased: true})
}

func cryptoKey(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Active key version", Unit: "version-months", Rate: "gcp/kms/key-version", Quantity: decimal.NewFromInt(1)})
	r.Add(costkit.Component{Name: "Cryptographic operations", Unit: "operations", Rate: "gcp/kms/operations", Quantity: r.Usage(usageKeyOperations, accessBand), UsageBased: true})
}

func artifactRepository(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Storage", Unit: "GiB-month", Rate: "gcp/artifactregistry/storage", Quantity: r.Usage(usageStorage, imagesBand), UsageBased: true})
}

func secret(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Active versions", Unit: "version-months", Rate: "gcp/secretmanager/version", Quantity: r.Usage(usageVersions, versionsBand), UsageBased: true})
	r.Add(costkit.Component{Name: "Access operations", Unit: "operations", Rate: "gcp/secretmanager/access", Quantity: r.Usage(usageAccess, accessBand), UsageBased: true})
}

func backendService(r *costkit.Subject) {
	if !r.Bool("enable_cdn") {
		r.Free()
		return
	}
	r.Add(costkit.Component{Name: "Cache egress", Unit: "GiB", Rate: "gcp/cdn/cache-egress", Quantity: r.Usage(usageDataOut, egressBand), UsageBased: true})
}

func forwardingRule(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Forwarding rule", Unit: "hours", Rate: "gcp/lb/forwarding-rule", Quantity: costkit.MonthlyHours})
	r.Add(costkit.Component{Name: "Data processed", Unit: "GiB", Rate: "gcp/lb/data-processed", Quantity: r.Usage(usageDataProcessed, egressBand), UsageBased: true})
}
