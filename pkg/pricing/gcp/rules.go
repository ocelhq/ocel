package gcp

import (
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pkg/pricing"
)

const (
	vendor = "gcp"

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

	ingressEverywhere  = "INGRESS_TRAFFIC_ALL"
	containerResources = "template.containers.0.resources"
	cpuIdle            = "template.containers.0.resources.cpu_idle"
	cpuLimit           = "template.containers.0.resources.limits.cpu"
	memoryLimit        = "template.containers.0.resources.limits.memory"
	minInstances       = "template.scaling.min_instance_count"
	mebibytesPerGiB    = 1024

	memorystoreAppendOnly    = "AOF"
	sqlPrivateServiceConnect = "settings.ip_configuration.psc_config.psc_enabled"

	sqlCustomMachine       = "db-custom-"
	sqlRegional            = "REGIONAL"
	sqlEnterprisePlus      = "ENTERPRISE_PLUS"
	sqlEnterprise          = "ENTERPRISE"
	sqlEnginePostgres      = "POSTGRES"
	sqlEnterprisePlusSince = 16
	sqlHardDisk            = "PD_HDD"
	sqlSolidState          = "PD_SSD"
	sqlHyperdisk           = "HYPERDISK_BALANCED"
	sqlSmallestDiskGB      = 10
	sqlSmallestHyperdiskGB = 20
	sqlZonal               = "ZONAL"
	secondsPerHour         = 3600
	schedulerJobDays       = 31
	messageRetention       = "message_retention_duration"
	subscriptionRetention  = "604800s"
	retainsAcked           = "retain_acked_messages"
	description            = "description"
	unitCount              = "count"
	unitHours              = "h"
	unitSeconds            = "s"
	unitGiB                = "GiBy"
	unitGiBSeconds         = "GiBy.s"
	unitGiBMonths          = "GiBy.mo"
	unitGiBHours           = "GiBy.h"
	unitMonths             = "mo"
	unitTiB                = "TiBy"
	unitGBHours            = "GBy.h"
	sqlMicroInstance       = "db-f1-micro"
	sqlSmallInstance       = "db-g1-small"
	sqlEngineMySQL         = "MYSQL"
	sqlEngineSQLServer     = "SQLSERVER"
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
	aliveBand    = pricing.Band{Light: 180, Moderate: 730, Heavy: 730}
	thousand     = decimal.NewFromInt(1000)
	kibPerTiB    = decimal.NewFromInt(1 << 30)
	kibPerGiB    = decimal.NewFromInt(1 << 20)

	memorystoreNodes = map[string]struct {
		name       string
		capacityGB float64
	}{
		"SHARED_CORE_NANO": {"Shared Core Nano", 1.4},
		"CUSTOM_PICO":      {"Custom Pico", 1.25},
		"CUSTOM_MICRO":     {"Custom Micro", 2.5},
		"CUSTOM_MINI":      {"Custom Mini", 3.75},
		"STANDARD_SMALL":   {"Standard Small", 6.5},
		"HIGHMEM_MEDIUM":   {"Highmem Medium", 13},
		"HIGHCPU_MEDIUM":   {"Highcpu Medium", 13},
		"STANDARD_LARGE":   {"Standard Large", 26},
		"HIGHMEM_XLARGE":   {"Highmem XLarge", 58},
		"HIGHMEM_2XLARGE":  {"Highmem 2xlarge", 110},
	}
	sqlEngines = map[string]string{sqlEngineMySQL: "MySQL", sqlEngineSQLServer: "SQL Server"}
)

var Rules = pricing.Rules{
	Table: pricing.Table{
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
		"google_compute_backend_bucket":                         free,
		"google_project_iam_member":                             free,
		"google_project_iam_binding":                            free,
		"google_service_account_iam_member":                     free,
		"google_network_services_wasm_plugin":                   free,
		"google_network_services_lb_edge_extension":             free,
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
		"google_pubsub_topic":                                   pubSubTopic,
		"google_pubsub_subscription":                            pubSubSubscription,
		"google_cloud_tasks_queue":                              tasksQueue,
		"google_tags_tag_key":                                   free,
		"google_logging_log_view":                               free,
	},
	Notes: []string{
		"the free tier Cloud Run and Cloud Storage apply as a billing discount is not applied",
		"the forwarding rule is priced as the project's first five, which share one hourly charge",
		"a Cloud Run service billed per instance is priced for every hour an instance lives, idle ones included: Cloud Run keeps an instance up to 15 minutes after its last request, so traffic that never pauses that long keeps one alive all month",
		"a minimum instance of a Cloud Run service billed per request is priced at the idle rate all month, on top of the requests it serves",
		"internet egress is priced as traffic to the continent the resource runs in",
	},
	Ingest: ingestion,
}

func regional(r *pricing.Subject, service, unit, text string) pricing.Query {
	return pricing.Query{Vendor: vendor, Service: service, Region: r.Region(), Unit: unit, Attributes: map[string]string{description: text}}
}

func global(service, unit, text string) pricing.Query {
	return pricing.Query{Vendor: vendor, Service: service, Unit: unit, Attributes: map[string]string{description: text}}
}

func free(r *pricing.Subject) { r.Free() }

const (
	requestCPU    = "Services CPU (Request-based billing)"
	requestMemory = "Services Memory (Request-based billing)"
	idleCPU       = "Services Min Instance CPU (Request-based billing)"
	idleMemory    = "Services Min Instance Memory (Request-based billing)"
)

var tier2 = map[string]string{
	requestCPU:    "Services CPU Tier 2  (Request-based billing)",
	requestMemory: "Services Memory Tier 2 (Request-based billing)",
	idleCPU:       "Services Min Instance CPU Tier 2 (Request-based billing)",
	idleMemory:    "Services Min Instance Memory Tier 2 (Request-based billing)",
}

func requestBased(r *pricing.Subject, unit, text string) pricing.Query {
	if locations[r.Region()].cloudRunTier2 {
		text = tier2[text]
	}
	return regional(r, serviceCloudRun, unit, text)
}

func cloudRunService(r *pricing.Subject) {
	cpu := func() decimal.Decimal { return r.QuantityOr(cpuLimit, cores, decimal.NewFromInt(1)) }
	memoryGiB := func() decimal.Decimal { return r.QuantityOr(memoryLimit, gibibytes, decimal.NewFromFloat(0.5)) }
	minimum := func() decimal.Decimal { return r.NumberOr(minInstances, decimal.Zero) }
	idlesBetweenRequests := func() bool {
		if r.Unknown(containerResources) || r.Has(containerResources+".0") {
			return r.BoolOr(cpuIdle, false)
		}
		return true
	}
	billing := []string{cpuIdle}
	if !idlesBetweenRequests() {
		seconds := func() decimal.Decimal {
			warm, _ := minimum().Mul(pricing.MonthlyHours).Float64()
			alive := pricing.Band{Light: max(warm, aliveBand.Light), Moderate: max(warm, aliveBand.Moderate), Heavy: max(warm, aliveBand.Heavy)}
			return r.Usage(usageInstanceHours, alive).Mul(decimal.NewFromInt(secondsPerHour))
		}
		r.Add(pricing.Component{Name: "CPU, always allocated", Unit: "vCPU-seconds", Needs: billing,
			Price: regional(r, serviceCloudRun, unitSeconds, "Services CPU (Instance-based billing)"), Quantity: seconds().Mul(cpu())})
		r.Add(pricing.Component{Name: "Memory, always allocated", Unit: "GiB-seconds", Needs: billing,
			Price: regional(r, serviceCloudRun, unitGiBSeconds, "Services Memory (Instance-based billing)"), Quantity: seconds().Mul(memoryGiB())})
	} else {
		seconds := func() decimal.Decimal {
			return r.Usage(usageRequests, requestsBand).Mul(r.Usage(usageRequestDuration, durationBand)).Div(thousand)
		}
		r.Add(pricing.Component{Name: "Requests", Unit: "requests", UsageBased: true, Needs: billing,
			Price: global(serviceCloudRun, unitCount, "Requests"), Quantity: r.Usage(usageRequests, requestsBand)})
		r.Add(pricing.Component{Name: "CPU during requests", Unit: "vCPU-seconds", UsageBased: true, Needs: billing,
			Price: requestBased(r, unitSeconds, requestCPU), Quantity: seconds().Mul(cpu())})
		r.Add(pricing.Component{Name: "Memory during requests", Unit: "GiB-seconds", UsageBased: true, Needs: billing,
			Price: requestBased(r, unitGiBSeconds, requestMemory), Quantity: seconds().Mul(memoryGiB())})
	}
	if r.When(func() bool { return minimum().IsPositive() }, idlesBetweenRequests) {
		seconds := func() decimal.Decimal {
			return minimum().Mul(pricing.MonthlyHours).Mul(decimal.NewFromInt(secondsPerHour))
		}
		idle := []string{cpuIdle, minInstances}
		r.Add(pricing.Component{Name: "CPU, idle minimum instances", Unit: "vCPU-seconds", Needs: idle,
			Price: requestBased(r, unitSeconds, idleCPU), Quantity: seconds().Mul(cpu())})
		r.Add(pricing.Component{Name: "Memory, idle minimum instances", Unit: "GiB-seconds", Needs: idle,
			Price: requestBased(r, unitGiBSeconds, idleMemory), Quantity: seconds().Mul(memoryGiB())})
	}
	if r.When(func() bool { return r.StringOr("ingress", ingressEverywhere) == ingressEverywhere }) {
		continent := locations[r.Region()].continent
		r.Add(pricing.Component{Name: "Data transfer out to internet", Unit: "GiB", UsageBased: true, Needs: []string{"ingress"},
			Price:    regional(r, serviceCloudRun, unitGiB, "Cloud Run Network Internet Data Transfer Out "+continent+" to "+continent),
			Quantity: r.Usage(usageDataOut, egressBand)})
	}
}

func cores(limit string) (decimal.Decimal, bool) {
	if millicores, ok := strings.CutSuffix(limit, "m"); ok {
		parsed, err := decimal.NewFromString(millicores)
		return parsed.Div(thousand), err == nil && !parsed.IsNegative()
	}
	parsed, err := decimal.NewFromString(limit)
	return parsed, err == nil && !parsed.IsNegative()
}

var byteSuffixes = []struct {
	suffix string
	bytes  decimal.Decimal
}{
	{"Ki", decimal.NewFromInt(1 << 10)},
	{"Mi", decimal.NewFromInt(1 << 20)},
	{"Gi", decimal.NewFromInt(1 << 30)},
	{"Ti", decimal.NewFromInt(1 << 40)},
	{"k", decimal.NewFromInt(1e3)},
	{"M", decimal.NewFromInt(1e6)},
	{"G", decimal.NewFromInt(1e9)},
	{"T", decimal.NewFromInt(1e12)},
}

func gibibytes(limit string) (decimal.Decimal, bool) {
	amount, scale := limit, decimal.NewFromInt(1)
	for _, unit := range byteSuffixes {
		if trimmed, ok := strings.CutSuffix(limit, unit.suffix); ok {
			amount, scale = trimmed, unit.bytes
			break
		}
	}
	parsed, err := decimal.NewFromString(amount)
	if err != nil || parsed.IsNegative() {
		return decimal.Zero, false
	}
	return parsed.Mul(scale).Div(decimal.NewFromInt(1 << 30)), true
}

func pubSubThroughput(name string) func(*pricing.Subject) {
	return func(r *pricing.Subject) {
		r.Add(pricing.Component{Name: name, Unit: "TiB", UsageBased: true,
			Price:    global(servicePubSub, unitTiB, "Message Delivery Basic"),
			Quantity: r.Usage(usageMessages, messagesBand).Div(kibPerTiB)})
	}
}

func pubSubTopic(r *pricing.Subject) {
	pubSubThroughput("Publish throughput")(r)
	if r.Unknown(messageRetention) || r.Has(messageRetention) {
		messageStorage(r, "Topics message backlog")
	}
}

func pubSubSubscription(r *pricing.Subject) {
	pubSubThroughput("Delivery throughput")(r)
	if r.Unknown(retainsAcked) || r.BoolOr(retainsAcked, false) {
		messageStorage(r, "Subscriptions retained acknowledged messages", retainsAcked)
	}
}

func messageStorage(r *pricing.Subject, sku string, needs ...string) {
	share := retainedShare(r.StringOr(messageRetention, subscriptionRetention))
	r.Add(pricing.Component{Name: "Message storage", Unit: "GiB-month", UsageBased: true, Needs: append(needs, messageRetention),
		Price:    global(servicePubSub, unitGiBMonths, sku),
		Quantity: r.Usage(usageMessages, messagesBand).Div(kibPerGiB).Mul(share)})
}

func retainedShare(retention string) decimal.Decimal {
	seconds, err := decimal.NewFromString(strings.TrimSuffix(retention, "s"))
	if err != nil || !strings.HasSuffix(retention, "s") {
		return decimal.Zero
	}
	return decimal.Min(seconds.Div(pricing.MonthlyHours.Mul(decimal.NewFromInt(secondsPerHour))), decimal.NewFromInt(1))
}

func tasksQueue(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Operations", Unit: "operations", UsageBased: true, Allowance: pricing.AllowanceAccount,
		Price: global(serviceTasks, unitCount, "Operations"), Quantity: r.Usage(usageTaskOperations, taskOpsBand)})
}

func schedulerJob(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Job", Unit: "job-days", Allowance: pricing.AllowanceAccount,
		Price: global(serviceScheduler, unitCount, "Jobs"), Quantity: decimal.NewFromInt(schedulerJobDays)})
}

func firestoreDatabase(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Document reads", Unit: "reads", UsageBased: true,
		Price: global(serviceFirestore, unitCount, "Cloud Firestore Read Ops "+locations[r.Region()].firestore), Quantity: r.Usage(usageReads, readsBand)})
	r.Add(pricing.Component{Name: "Document writes", Unit: "writes", UsageBased: true,
		Price: global(serviceFirestore, unitCount, "Cloud Firestore Entity Writes "+locations[r.Region()].firestore), Quantity: r.Usage(usageWrites, writesBand)})
	r.Add(pricing.Component{Name: "Storage", Unit: "GiB-month", UsageBased: true,
		Price: global(serviceFirestore, unitGiBMonths, "Cloud Firestore Storage "+locations[r.Region()].firestore), Quantity: r.Usage(usageStorage, recordsBand)})
}

func storageBucket(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Storage", Unit: "GiB-month", UsageBased: true,
		Price:    regional(r, serviceStorage, unitGiBMonths, "Standard Storage "+locations[r.Region()].storage),
		Quantity: r.Usage(usageStorage, storageBand)})
	r.Add(pricing.Component{Name: "Class A operations", Unit: "operations", UsageBased: true, Allowance: pricing.AllowanceAccount,
		Price: global(serviceStorage, unitCount, "Regional Standard Class A Operations"), Quantity: r.Usage(usageClassA, classABand)})
	r.Add(pricing.Component{Name: "Class B operations", Unit: "operations", UsageBased: true, Allowance: pricing.AllowanceAccount,
		Price: global(serviceStorage, unitCount, "Regional Standard Class B Operations"), Quantity: r.Usage(usageClassB, classBBand)})
}

func cryptoKey(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Active key version", Unit: "version-months", Quantity: decimal.NewFromInt(1),
		Price: regional(r, serviceKMS, unitMonths, "Active software symmetric key versions")})
	r.Add(pricing.Component{Name: "Cryptographic operations", Unit: "operations", UsageBased: true,
		Price:    regional(r, serviceKMS, unitCount, "Cryptographic operations with a software symmetric key"),
		Quantity: r.Usage(usageKeyOperations, accessBand)})
}

func artifactRepository(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Storage", Unit: "GiB-month", UsageBased: true, Allowance: pricing.AllowanceAccount,
		Price: global(serviceArtifactRegistry, unitGiBMonths, "Artifact Registry Storage"), Quantity: r.Usage(usageStorage, imagesBand)})
}

func secret(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Active versions", Unit: "version-months", UsageBased: true, Allowance: pricing.AllowanceAccount,
		Price: regional(r, serviceSecretManager, unitMonths, "Secret version replica storage"), Quantity: r.Usage(usageVersions, versionsBand)})
	r.Add(pricing.Component{Name: "Access operations", Unit: "operations", UsageBased: true, Allowance: pricing.AllowanceAccount,
		Price: regional(r, serviceSecretManager, unitCount, "Secret access operations"), Quantity: r.Usage(usageAccess, accessBand)})
}

func backendService(r *pricing.Subject) {
	if !r.When(func() bool { return r.BoolOr("enable_cdn", false) }) {
		r.Free()
		return
	}
	r.Add(pricing.Component{Name: "Cache egress", Unit: "GiB", UsageBased: true, Needs: []string{"enable_cdn"},
		Price:    global(serviceNetworking, unitGiB, "Networking Cloud CDN Traffic Cache Data Transfer to "+locations[r.Region()].cdnDestination),
		Quantity: r.Usage(usageDataOut, egressBand)})
}

func memorystoreInstance(r *pricing.Subject) {
	r.DeclareComputed("node_type")
	nodes := func() decimal.Decimal {
		return r.Number("shard_count").Mul(r.NumberOr("replica_count", decimal.Zero).Add(decimal.NewFromInt(1)))
	}
	r.Add(pricing.Component{Name: "Node", Unit: "node-hours", Quantity: pricing.MonthlyHours.Mul(nodes()),
		Price: regional(r, serviceMemorystore, unitHours, memorystoreNodes[r.String("node_type")].name+" Node "+locations[r.Region()].memorystore)})
	if r.When(func() bool { return r.StringOr("persistence_config.mode", "") == memorystoreAppendOnly }) {
		r.Add(pricing.Component{Name: "Append-only persistence", Unit: "GB-hours", Needs: []string{"persistence_config.mode"},
			Price:    regional(r, serviceMemorystore, unitGBHours, "Memorystore for Valkey: AOF Storage "+locations[r.Region()].memorystore),
			Quantity: r.Quantity("node_type", nodeCapacityGB).Mul(nodes()).Mul(pricing.MonthlyHours)})
	}
	r.Add(pricing.Component{Name: "Inter-zone data processed", Unit: "GiB", UsageBased: true,
		Price:    global(serviceNetworking, unitGiB, "Networking Private Service Connect Consumer Data Processing"),
		Quantity: r.Usage(usageDataProcessed, egressBand)})
}

func nodeCapacityGB(nodeType string) (decimal.Decimal, bool) {
	node, ok := memorystoreNodes[nodeType]
	return decimal.NewFromFloat(node.capacityGB), ok
}

func sqlDescription(r *pricing.Subject, kind string) string {
	engine := "PostgreSQL"
	if prefix, _, _ := strings.Cut(r.String("database_version"), "_"); sqlEngines[prefix] != "" {
		engine = sqlEngines[prefix]
	}
	availability := "Zonal"
	if r.StringOr("settings.availability_type", sqlZonal) == sqlRegional {
		availability = "Regional"
	}
	if r.StringOr("settings.edition", sqlDefaultEdition(r)) == sqlEnterprisePlus {
		kind = "Enterprise Plus " + kind
	}
	return "Cloud SQL for " + engine + ": " + availability + " - " + kind + " in " + locations[r.Region()].sql
}

func sqlDefaultEdition(r *pricing.Subject) string {
	engine, version, _ := strings.Cut(r.String("database_version"), "_")
	if major, err := strconv.Atoi(version); err == nil && engine == sqlEnginePostgres && major >= sqlEnterprisePlusSince {
		return sqlEnterprisePlus
	}
	return sqlEnterprise
}

func sqlInstance(r *pricing.Subject) {
	machine := r.String("settings.tier")
	switch vcpus, memory, custom := customMachine(machine); {
	case custom:
		r.Add(pricing.Component{Name: "vCPU", Unit: "vCPU-hours", Quantity: pricing.MonthlyHours.Mul(vcpus),
			Price: regional(r, serviceSQL, unitHours, sqlDescription(r, "vCPU"))})
		r.Add(pricing.Component{Name: "Memory", Unit: "GiB-hours", Quantity: pricing.MonthlyHours.Mul(memory),
			Price: regional(r, serviceSQL, unitGiBHours, sqlDescription(r, "RAM"))})
	case machine == sqlMicroInstance:
		r.Add(pricing.Component{Name: "Instance", Unit: "hours", Quantity: pricing.MonthlyHours,
			Price: regional(r, serviceSQL, unitHours, sqlDescription(r, "Micro instance"))})
	default:
		r.Add(pricing.Component{Name: "Instance", Unit: "hours", Quantity: pricing.MonthlyHours,
			Price: regional(r, serviceSQL, unitHours, sqlDescription(r, sqlInstanceKind(machine)))})
	}
	disk, smallest := "Standard storage", decimal.NewFromInt(sqlSmallestDiskGB)
	switch r.StringOr("settings.disk_type", sqlSolidState) {
	case sqlHardDisk:
		disk = "Low cost storage"
	case sqlHyperdisk:
		smallest = decimal.NewFromInt(sqlSmallestHyperdiskGB)
	}
	r.Add(pricing.Component{Name: "Storage", Unit: "GiB-month", Quantity: r.NumberOr("settings.disk_size", smallest),
		Price: regional(r, serviceSQL, unitGiBMonths, sqlDescription(r, disk))})
	r.Add(pricing.Component{Name: "Backups", Unit: "GiB-month", UsageBased: true,
		Price:    regional(r, serviceSQL, unitGiBMonths, "Cloud SQL: Backups in "+locations[r.Region()].sql),
		Quantity: r.Usage(usageStorage, storageBand)})
	if r.When(func() bool { return r.BoolOr(sqlPrivateServiceConnect, false) }) {
		psc := []string{sqlPrivateServiceConnect}
		r.Add(pricing.Component{Name: "Private Service Connect endpoint", Unit: "hours", Quantity: pricing.MonthlyHours, Needs: psc,
			Price: global(serviceNetworking, unitHours, "Networking Private Service Connect Consumer End Point")})
		r.Add(pricing.Component{Name: "Data processed", Unit: "GiB", UsageBased: true, Needs: psc,
			Price:    global(serviceNetworking, unitGiB, "Networking Private Service Connect Consumer Data Processing"),
			Quantity: r.Usage(usageDataProcessed, egressBand)})
	}
}

func sqlInstanceKind(machine string) string {
	if machine == sqlSmallInstance {
		return "Small instance"
	}
	return machine
}

func customMachine(machine string) (vcpus, memoryGiB decimal.Decimal, custom bool) {
	shape, custom := strings.CutPrefix(machine, sqlCustomMachine)
	if !custom {
		return decimal.Zero, decimal.Zero, false
	}
	cores, mebibytes, split := strings.Cut(shape, "-")
	vcpus, coresErr := decimal.NewFromString(cores)
	memory, memoryErr := decimal.NewFromString(mebibytes)
	if !split || coresErr != nil || memoryErr != nil {
		return decimal.Zero, decimal.Zero, false
	}
	return vcpus, memory.Div(decimal.NewFromInt(mebibytesPerGiB)), true
}

func forwardingRule(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Forwarding rule", Unit: "hours", Quantity: pricing.MonthlyHours,
		Price: global(serviceNetworking, unitHours, "Cloud Load Balancer Forwarding Rule Minimum Global")})
	r.Add(pricing.Component{Name: "Data processed", Unit: "GiB", UsageBased: true,
		Price: regional(r, serviceNetworking, unitGiB,
			"Global External Application Load Balancer Inbound Data Processing for "+locations[r.Region()].network+" ("+r.Region()+")"),
		Quantity: r.Usage(usageDataProcessed, egressBand)})
}
