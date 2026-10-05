package cost

import (
	_ "embed"
	"sync"

	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pkg/pricing"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

//go:embed rates.json
var rates []byte

var card = sync.OnceValues(func() (*pricing.Card, error) { return pricing.Load(rates) })

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
	usageMessages        = "monthly_messages"
	usageTaskOperations  = "monthly_task_operations"
	usageConnectedHours  = "monthly_connected_hours"

	ingressEverywhere = "INGRESS_TRAFFIC_ALL"
	cpuIdle           = "template.containers.0.resources.cpu_idle"
	scheduledRequests = "requests_per_hour"
	billedSecondsEach = "billed_seconds_per_request"
	holdsSockets      = "holds_sockets"
	deadLetter        = "dead_letter"
	mebibytesPerGiB   = 1024
	secondsPerHour    = 3600
)

var (
	requestsBand = pricing.Band{Light: 100_000, Moderate: 1_000_000, Heavy: 10_000_000}
	durationBand = pricing.Band{Light: 100, Moderate: 200, Heavy: 500}
	storageBand  = pricing.Band{Light: 1, Moderate: 10, Heavy: 100}
	classABand   = pricing.Band{Light: 1_000, Moderate: 10_000, Heavy: 100_000}
	classBBand   = pricing.Band{Light: 10_000, Moderate: 100_000, Heavy: 1_000_000}
	readsBand    = pricing.Band{Light: 100_000, Moderate: 1_000_000, Heavy: 10_000_000}
	writesBand   = pricing.Band{Light: 10_000, Moderate: 100_000, Heavy: 1_000_000}
	recordsBand  = pricing.Band{Light: 0.1, Moderate: 1, Heavy: 10}
	accessBand   = pricing.Band{Light: 1_000, Moderate: 10_000, Heavy: 100_000}
	egressBand   = pricing.Band{Light: 1, Moderate: 10, Heavy: 100}
	imagesBand   = pricing.Band{Light: 1, Moderate: 5, Heavy: 50}
	versionsBand = pricing.Band{Light: 1, Moderate: 1, Heavy: 3}
	messagesBand = pricing.Band{Light: 100_000, Moderate: 1_000_000, Heavy: 10_000_000}
	taskOpsBand  = pricing.Band{Light: 10_000, Moderate: 100_000, Heavy: 1_000_000}
	socketsBand  = pricing.Band{Light: 180, Moderate: 730, Heavy: 730}
	thousand     = decimal.NewFromInt(1000)
	kibPerGiB    = decimal.NewFromInt(1 << 20)
)

var formulas = pricing.Table{
	"google_cloud_run_v2_service":                           cloudRunService,
	"google_cloud_scheduler_job":                            schedulerJob,
	"google_firestore_database":                             firestoreDatabase,
	"google_storage_bucket":                                 storageBucket,
	"google_kms_key_ring":                                   free,
	"google_kms_crypto_key":                                 cryptoKey,
	"google_service_account":                                free,
	"google_project_iam_custom_role":                        free,
	"google_project_service_identity":                       free,
	"google_artifact_registry_repository":                   artifactRepository,
	"google_secret_manager_secret":                          secret,
	"google_compute_global_address":                         free,
	"google_compute_backend_service":                        backendService,
	"google_certificate_manager_certificate_map":            free,
	"google_certificate_manager_certificate_map_entry":      free,
	"google_compute_url_map":                                free,
	"google_compute_target_https_proxy":                     free,
	"google_compute_global_forwarding_rule":                 forwardingRule,
	"google_compute_region_network_endpoint_group":          free,
	"google_memorystore_instance":                           memorystoreInstance,
	"google_sql_database_instance":                          sqlInstance,
	"google_compute_network":                                free,
	"google_compute_subnetwork":                             free,
	"google_network_connectivity_service_connection_policy": free,
	"google_pubsub_topic":                                   pubSubThroughput("Publish throughput"),
	"google_pubsub_subscription":                            pubSubThroughput("Delivery throughput"),
	"google_cloud_tasks_queue":                              tasksQueue,
}

func Price(req *costv1.PriceRequest, edges ...pricing.EdgeRates) (*costv1.Estimate, error) {
	rateCard, err := card()
	if err != nil {
		return nil, err
	}
	merged, table, err := pricing.Priced(rateCard, formulas, edges...)
	if err != nil {
		return nil, err
	}
	estimate, err := pricing.Estimate(merged, table, req)
	if err != nil {
		return nil, err
	}
	estimate.Notes = append(estimate.Notes,
		"list prices for the regions the card names and for global SKUs; a resource elsewhere is left unpriced unless a note above says otherwise",
		"the free tier Cloud Run and Cloud Storage apply as a billing discount is not applied; a free first tier the catalog publishes is spent once per account across every resource sharing it",
		"the forwarding rule is priced as the project's first five, which share one hourly charge",
		"a realtime gateway is priced at Cloud Run's request-based rate for every hour a socket is open, an upper bound on what an instance holding sockets bills",
	)
	return estimate, nil
}

func free(r *pricing.Subject) { r.Free() }

func cloudRunService(r *pricing.Subject) {
	cpu := func() decimal.Decimal { return r.Number("template.containers.0.resources.limits.cpu") }
	memoryGiB := func() decimal.Decimal {
		return quantityMiB(r.String("template.containers.0.resources.limits.memory")).Div(decimal.NewFromInt(mebibytesPerGiB))
	}
	billing := []string{cpuIdle}
	switch {
	case !r.Bool(cpuIdle):
		seconds := func() decimal.Decimal {
			warm, _ := r.Number("template.scaling.min_instance_count").Mul(pricing.MonthlyHours).Float64()
			return r.Usage(usageInstanceHours, pricing.Band{Light: warm, Moderate: warm, Heavy: warm}).Mul(decimal.NewFromInt(secondsPerHour))
		}
		r.Add(pricing.Component{Name: "CPU, always allocated", Unit: "vCPU-seconds", Rate: "gcp/run/cpu-always", Quantity: seconds().Mul(cpu()), Needs: billing})
		r.Add(pricing.Component{Name: "Memory, always allocated", Unit: "GiB-seconds", Rate: "gcp/run/memory-always", Quantity: seconds().Mul(memoryGiB()), Needs: billing})
	case r.Bool(holdsSockets):
		seconds := func() decimal.Decimal {
			return r.Usage(usageConnectedHours, socketsBand).Mul(decimal.NewFromInt(secondsPerHour))
		}
		r.Add(pricing.Component{Name: "Requests", Unit: "requests", Rate: "gcp/run/requests", Quantity: r.Usage(usageRequests, requestsBand), UsageBased: true, Needs: billing})
		r.Add(pricing.Component{Name: "CPU while sockets are open", Unit: "vCPU-seconds", Rate: "gcp/run/cpu-active", UsageBased: true,
			Quantity: seconds().Mul(cpu()), Needs: billing})
		r.Add(pricing.Component{Name: "Memory while sockets are open", Unit: "GiB-seconds", Rate: "gcp/run/memory-active", UsageBased: true,
			Quantity: seconds().Mul(memoryGiB()), Needs: billing})
	case r.Has(scheduledRequests):
		requests := r.Number(scheduledRequests).Mul(pricing.MonthlyHours)
		seconds := requests.Mul(r.Number(billedSecondsEach))
		r.Add(pricing.Component{Name: "Requests", Unit: "requests", Rate: "gcp/run/requests", Quantity: requests, Needs: billing})
		r.Add(pricing.Component{Name: "CPU during requests", Unit: "vCPU-seconds", Rate: "gcp/run/cpu-active", Quantity: seconds.Mul(cpu()), Needs: billing})
		r.Add(pricing.Component{Name: "Memory during requests", Unit: "GiB-seconds", Rate: "gcp/run/memory-active", Quantity: seconds.Mul(memoryGiB()), Needs: billing})
	default:
		seconds := func() decimal.Decimal {
			return r.Usage(usageRequests, requestsBand).Mul(r.Usage(usageRequestDuration, durationBand)).Div(thousand)
		}
		r.Add(pricing.Component{Name: "Requests", Unit: "requests", Rate: "gcp/run/requests", Quantity: r.Usage(usageRequests, requestsBand), UsageBased: true, Needs: billing})
		r.Add(pricing.Component{Name: "CPU during requests", Unit: "vCPU-seconds", Rate: "gcp/run/cpu-active", UsageBased: true,
			Quantity: seconds().Mul(cpu()), Needs: billing})
		r.Add(pricing.Component{Name: "Memory during requests", Unit: "GiB-seconds", Rate: "gcp/run/memory-active", UsageBased: true,
			Quantity: seconds().Mul(memoryGiB()), Needs: billing})
	}
	if r.String("ingress") == ingressEverywhere {
		r.Add(pricing.Component{Name: "Data transfer out to internet", Unit: "GiB", Rate: "gcp/network/premium-egress", Quantity: r.Usage(usageDataOut, egressBand), UsageBased: true})
	}
}

func pubSubThroughput(name string) func(*pricing.Subject) {
	return func(r *pricing.Subject) {
		if r.Bool(deadLetter) {
			r.Free()
			return
		}
		r.Add(pricing.Component{Name: name, Unit: "GiB", Rate: "gcp/pubsub/throughput", UsageBased: true,
			Quantity: r.Usage(usageMessages, messagesBand).Div(kibPerGiB)})
	}
}

func tasksQueue(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Operations", Unit: "operations", Rate: "gcp/tasks/operations", UsageBased: true,
		Quantity: r.Usage(usageTaskOperations, taskOpsBand)})
}

func schedulerJob(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Job", Unit: "job-months", Rate: "gcp/scheduler/job", Quantity: decimal.NewFromInt(1)})
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

func firestoreDatabase(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Document reads", Unit: "reads", Rate: "gcp/firestore/reads", Quantity: r.Usage(usageReads, readsBand), UsageBased: true})
	r.Add(pricing.Component{Name: "Document writes", Unit: "writes", Rate: "gcp/firestore/writes", Quantity: r.Usage(usageWrites, writesBand), UsageBased: true})
	r.Add(pricing.Component{Name: "Storage", Unit: "GiB-month", Rate: "gcp/firestore/storage", Quantity: r.Usage(usageStorage, recordsBand), UsageBased: true})
}

func storageBucket(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Storage", Unit: "GiB-month", Rate: "gcp/storage/standard", Quantity: r.Usage(usageStorage, storageBand), UsageBased: true})
	r.Add(pricing.Component{Name: "Class A operations", Unit: "operations", Rate: "gcp/storage/class-a", Quantity: r.Usage(usageClassA, classABand), UsageBased: true})
	r.Add(pricing.Component{Name: "Class B operations", Unit: "operations", Rate: "gcp/storage/class-b", Quantity: r.Usage(usageClassB, classBBand), UsageBased: true})
}

func cryptoKey(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Active key version", Unit: "version-months", Rate: "gcp/kms/key-version", Quantity: decimal.NewFromInt(1)})
	r.Add(pricing.Component{Name: "Cryptographic operations", Unit: "operations", Rate: "gcp/kms/operations", Quantity: r.Usage(usageKeyOperations, accessBand), UsageBased: true})
}

func artifactRepository(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Storage", Unit: "GiB-month", Rate: "gcp/artifactregistry/storage", Quantity: r.Usage(usageStorage, imagesBand), UsageBased: true})
}

func secret(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Active versions", Unit: "version-months", Rate: "gcp/secretmanager/version", Quantity: r.Usage(usageVersions, versionsBand), UsageBased: true})
	r.Add(pricing.Component{Name: "Access operations", Unit: "operations", Rate: "gcp/secretmanager/access", Quantity: r.Usage(usageAccess, accessBand), UsageBased: true})
}

func backendService(r *pricing.Subject) {
	if !r.Bool("enable_cdn") {
		r.Free()
		return
	}
	r.Add(pricing.Component{Name: "Cache egress", Unit: "GiB", Rate: "gcp/cdn/cache-egress", Quantity: r.Usage(usageDataOut, egressBand), UsageBased: true})
}

func memorystoreInstance(r *pricing.Subject) {
	nodes := r.Number("replica_count").Add(decimal.NewFromInt(1))
	r.Add(pricing.Component{Name: "Node", Unit: "node-hours", Rate: "gcp/memorystore/" + r.String("node_type"), Quantity: pricing.MonthlyHours.Mul(nodes)})
	if r.String("persistence_config.mode") == "AOF" {
		r.Add(pricing.Component{Name: "Append-only persistence", Unit: "GB-hours", Rate: "gcp/memorystore/aof",
			Quantity: r.Number("node_capacity_gb").Mul(nodes).Mul(pricing.MonthlyHours)})
	}
	r.Add(pricing.Component{Name: "Inter-zone data processed", Unit: "GiB", Rate: "gcp/psc/consumer-data-processing", Quantity: r.Usage(usageDataProcessed, egressBand), UsageBased: true})
}

func sqlInstance(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Instance", Unit: "hours", Rate: "gcp/sql/" + r.String("settings.tier"), Quantity: pricing.MonthlyHours})
	r.Add(pricing.Component{Name: "SSD storage", Unit: "GiB-month", Rate: "gcp/sql/ssd", Quantity: r.Number("settings.disk_size")})
	r.Add(pricing.Component{Name: "Backups", Unit: "GiB-month", Rate: "gcp/sql/backup", Quantity: r.Usage(usageStorage, storageBand), UsageBased: true})
	r.Add(pricing.Component{Name: "Private Service Connect endpoint", Unit: "hours", Rate: "gcp/psc/endpoint", Quantity: pricing.MonthlyHours})
	r.Add(pricing.Component{Name: "Data processed", Unit: "GiB", Rate: "gcp/psc/consumer-data-processing", Quantity: r.Usage(usageDataProcessed, egressBand), UsageBased: true})
}

func forwardingRule(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Forwarding rule", Unit: "hours", Rate: "gcp/lb/forwarding-rule", Quantity: pricing.MonthlyHours})
	r.Add(pricing.Component{Name: "Data processed", Unit: "GiB", Rate: "gcp/lb/data-processed", Quantity: r.Usage(usageDataProcessed, egressBand), UsageBased: true})
}
