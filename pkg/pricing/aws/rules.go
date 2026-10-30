package aws

import (
	"strconv"
	"strings"

	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pkg/pricing"
)

const (
	vendor = "aws"

	usageRequests          = "monthly_requests"
	usageRequestDuration   = "request_duration_ms"
	usageStorage           = "storage_gb"
	usageLogsIngested      = "monthly_data_ingested_gb"
	usageS3Storage         = "standard.storage_gb"
	usageS3Tier1Requests   = "standard.monthly_tier_1_requests"
	usageS3Tier2Requests   = "standard.monthly_tier_2_requests"
	usageCapacityUnits     = "capacity_units_per_hr"
	usageWritesPerSecond   = "write_requests_per_sec"
	usageReadsPerSecond    = "read_requests_per_sec"
	usageNewConnections    = "new_connections"
	usageActiveConnections = "active_connections"
	usageProcessedBytes    = "processed_bytes_gb"
	usageRuleEvaluations   = "rule_evaluations"
	usageWriteUnits        = "monthly_write_request_units"
	usageReadUnits         = "monthly_read_request_units"
	usageCloudFrontOut     = "monthly_data_transfer_to_internet_gb.us"
	usageHTTPSRequests     = "monthly_https_requests.us"
	usageInvocations       = "monthly_invocations"
	usageOutboundInternet  = "monthly_outbound_internet_gb"
	usageSnapshotStorage   = "snapshot_storage_gb"
	usageEventOperations   = "monthly_operations"
	usageConnectionMinutes = "monthly_connection_minutes"
	usageDataProcessed     = "monthly_data_processed_gb"
	usageStoreReads        = "monthly_reads"
	usageStoreAPICalls     = "monthly_api_calls"

	elastiCacheFreeSnapshots = 1
	cacheEngineDefault       = "redis"

	lambdaGB        = 1024
	lambdaDefaultMB = 128
	lambdaX86       = "x86_64"
	fargateCPUUnits = 1024
	fargateMemoryMB = 1024
	fargateX86      = "X86_64"
	secondsPerHour  = 3600

	fargateLaunchType   = "FARGATE"
	fargateSpotProvider = "FARGATE_SPOT"
	capacityStrategy    = "capacity_provider_strategy"

	lcuNewConnectionsPerSecond    = 25
	lcuActiveConnectionsPerMinute = 3000
	lcuProcessedGBPerHour         = 1
	lcuRuleEvaluationsPerSecond   = 1000

	serverlessClass = "db.serverless"
	usagetype       = "usagetype"
)

var (
	requestsBand   = pricing.Band{Light: 100_000, Moderate: 1_000_000, Heavy: 10_000_000}
	durationBand   = pricing.Band{Light: 100, Moderate: 200, Heavy: 500}
	storageBand    = pricing.Band{Light: 1, Moderate: 10, Heavy: 100}
	logsBand       = pricing.Band{Light: 0.1, Moderate: 1, Heavy: 10}
	logStorageBand = pricing.Band{Light: 0.05, Moderate: 0.5, Heavy: 5}
	writesBand     = pricing.Band{Light: 1_000, Moderate: 10_000, Heavy: 100_000}
	readsBand      = pricing.Band{Light: 10_000, Moderate: 100_000, Heavy: 1_000_000}
	writeRateBand  = pricing.Band{Light: 0.1, Moderate: 1, Heavy: 10}
	readRateBand   = pricing.Band{Light: 0.3, Moderate: 3, Heavy: 30}
	connectionBand = pricing.Band{Light: 1, Moderate: 10, Heavy: 100}
	activeBand     = pricing.Band{Light: 100, Moderate: 1_000, Heavy: 10_000}
	ruleBand       = pricing.Band{Light: 1, Moderate: 10, Heavy: 100}
	egressBand     = pricing.Band{Light: 1, Moderate: 10, Heavy: 100}
	tableWriteBand = pricing.Band{Light: 10_000, Moderate: 100_000, Heavy: 1_000_000}
	tableReadBand  = pricing.Band{Light: 10_000, Moderate: 100_000, Heavy: 1_000_000}
	tableStoreBand = pricing.Band{Light: 0.1, Moderate: 1, Heavy: 10}
	apiCallsBand   = pricing.Band{Light: 1_000, Moderate: 10_000, Heavy: 100_000}
	imagesBand     = pricing.Band{Light: 1, Moderate: 5, Heavy: 50}
	scheduleBand   = pricing.Band{Light: 1_440, Moderate: 43_200, Heavy: 1_000_000}
	minutesBand    = pricing.Band{Light: 100_000, Moderate: 1_000_000, Heavy: 10_000_000}
	keyUpdateBand  = pricing.Band{Light: 100, Moderate: 1_000, Heavy: 10_000}

	thousand        = decimal.NewFromInt(1000)
	secondsPerMonth = pricing.MonthlyHours.Mul(decimal.NewFromInt(secondsPerHour))

	databaseEngines = map[string]string{"aurora-postgresql": "Aurora PostgreSQL", "aurora-mysql": "Aurora MySQL"}
	storageEngines  = map[string]string{"aurora-postgresql": "Aurora PostgreSQL", "aurora-mysql": "Any"}
	cacheEngines    = map[string]string{"valkey": "Valkey", "redis": "Redis"}
)

var Rules = pricing.Rules{
	Table: pricing.Table{
		"aws_lambda_function":             lambdaFunction,
		"aws_lambda_function_url":         free,
		"aws_lambda_layer_version":        free,
		"aws_lambda_event_source_mapping": free,
		"aws_cloudwatch_log_group":        logGroup,
		"aws_s3_bucket":                   bucket,
		"aws_rds_cluster":                 auroraCluster,
		"aws_rds_cluster_instance":        auroraInstance,
		"aws_secretsmanager_secret":       secret,
		"aws_ecs_cluster":                 free,
		"aws_ecs_task_definition":         free,
		"aws_ecs_service":                 fargateService,
		"aws_lb":                          loadBalancer,
		"aws_nat_gateway":                 natGateway,
		"aws_eip":                         publicAddress,
		"aws_vpc":                         free,
		"aws_subnet":                      free,
		"aws_internet_gateway":            free,
		"aws_vpc_endpoint":                free,
		"aws_dynamodb_table":              dynamoTable,
		"aws_kms_key":                     kmsKey,
		"aws_sqs_queue":                   queue,
		"aws_sns_topic":                   topic,
		"aws_scheduler_schedule":          schedule,
		"aws_cloudwatch_metric_alarm":     metricAlarm,
		"aws_cloudfront_function":         cloudFrontFunction,
		"aws_cloudfront_key_value_store":  keyValueStore,
		"aws_cloudfront_distribution":     distribution,
		"aws_api_gateway_rest_api":        restAPI,
		"aws_appsync_api":                 eventAPI,
		"aws_ecr_repository":              registry,
		"aws_data_transfer":               dataTransfer,

		"aws_elasticache_replication_group": replicationGroup,
		"aws_elasticache_parameter_group":   free,
		"aws_elasticache_subnet_group":      free,
		"aws_ssm_parameter":                 free,
		"aws_iam_role":                      free,
		"aws_iam_policy":                    free,
		"aws_iam_role_policy":               free,
		"aws_iam_role_policy_attachment":    free,
		"aws_iam_instance_profile":          free,
	},
	Notes: []string{
		"an always-free allowance the price list leaves out, such as Lambda's, is not applied",
		"CloudFront is priced at its United States rates whichever price class the distribution uses",
	},
	Ingest: ingestion,
}

func regional(r *pricing.Subject, service string, attributes map[string]string) pricing.Query {
	return pricing.Query{Vendor: vendor, Service: service, Region: r.Region(), Attributes: attributes}
}

func global(service string, attributes map[string]string) pricing.Query {
	return pricing.Query{Vendor: vendor, Service: service, Attributes: attributes}
}

func free(r *pricing.Subject) { r.Free() }

func arm(r *pricing.Subject) string {
	if r.StringOr("architectures.0", lambdaX86) == "arm64" {
		return "-ARM"
	}
	return ""
}

func lambdaFunction(r *pricing.Subject) {
	suffix := arm(r)
	r.Add(pricing.Component{Name: "Requests", Unit: "requests", UsageBased: true,
		Price:    regional(r, serviceLambda, map[string]string{"group": "AWS-Lambda-Requests" + suffix, usagetype: "Request" + suffix}),
		Quantity: r.Usage(usageRequests, requestsBand)})
	suffix = arm(r)
	seconds := r.Usage(usageRequests, requestsBand).Mul(r.Usage(usageRequestDuration, durationBand)).Div(thousand)
	r.Add(pricing.Component{Name: "Duration", Unit: "GB-seconds", UsageBased: true,
		Price:    regional(r, serviceLambda, map[string]string{"group": "AWS-Lambda-Duration" + suffix, usagetype: "Lambda-GB-Second" + suffix}),
		Quantity: seconds.Mul(r.NumberOr("memory_size", decimal.NewFromInt(lambdaDefaultMB))).Div(decimal.NewFromInt(lambdaGB))})
}

func logGroup(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Ingested", Unit: "GB", UsageBased: true,
		Price:    regional(r, serviceCloudWatch, map[string]string{usagetype: "DataProcessing-Bytes", "version": "Current"}),
		Quantity: r.Usage(usageLogsIngested, logsBand)})
	r.Add(pricing.Component{Name: "Stored", Unit: "GB-month", UsageBased: true,
		Price:    regional(r, serviceCloudWatch, map[string]string{usagetype: "TimedStorage-ByteHrs", "version": "Current"}),
		Quantity: r.Usage(usageStorage, logStorageBand)})
}

func bucket(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Storage", Unit: "GB-month", UsageBased: true,
		Price:    regional(r, serviceS3, map[string]string{usagetype: "TimedStorage-ByteHrs", "volumeType": "Standard"}),
		Quantity: r.Usage(usageS3Storage, storageBand)})
	r.Add(pricing.Component{Name: "PUT, COPY, POST, LIST requests", Unit: "requests", UsageBased: true,
		Price:    regional(r, serviceS3, map[string]string{"group": "S3-API-Tier1", usagetype: "Requests-Tier1"}),
		Quantity: r.Usage(usageS3Tier1Requests, writesBand)})
	r.Add(pricing.Component{Name: "GET and other requests", Unit: "requests", UsageBased: true,
		Price:    regional(r, serviceS3, map[string]string{"group": "S3-API-Tier2", usagetype: "Requests-Tier2"}),
		Quantity: r.Usage(usageS3Tier2Requests, readsBand)})
}

func databaseEngine(r *pricing.Subject) string {
	engine := r.String("engine")
	if name, ok := databaseEngines[engine]; ok {
		return name
	}
	return engine
}

func storageEngine(r *pricing.Subject) string {
	engine := r.String("engine")
	if name, ok := storageEngines[engine]; ok {
		return name
	}
	return engine
}

func auroraCluster(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Storage", Unit: "GB-month", UsageBased: true,
		Price:    regional(r, serviceRDS, map[string]string{usagetype: "Aurora:StorageUsage", "databaseEngine": storageEngine(r)}),
		Quantity: r.Usage(usageStorage, storageBand)})
	perSecond := r.Usage(usageWritesPerSecond, writeRateBand).Add(r.Usage(usageReadsPerSecond, readRateBand))
	r.Add(pricing.Component{Name: "I/O requests", Unit: "requests", UsageBased: true,
		Price:    regional(r, serviceRDS, map[string]string{usagetype: "Aurora:StorageIOUsage", "databaseEngine": storageEngine(r)}),
		Quantity: perSecond.Mul(secondsPerMonth)})
}

func auroraInstance(r *pricing.Subject) {
	if class := r.String("instance_class"); class != serverlessClass {
		r.Add(pricing.Component{Name: "Instance", Unit: "hours", Quantity: pricing.MonthlyHours,
			Price: regional(r, serviceRDS, map[string]string{usagetype: "InstanceUsage:" + class, "databaseEngine": databaseEngine(r)})})
		return
	}
	cluster, _ := r.Reference("cluster_identifier")
	minimum := cluster.Number("serverlessv2_scaling_configuration.min_capacity")
	maximum := cluster.Number("serverlessv2_scaling_configuration.max_capacity")
	middle, _ := minimum.Add(maximum).Div(decimal.NewFromInt(2)).Float64()
	low, _ := minimum.Float64()
	high, _ := maximum.Float64()
	acu := r.Usage(usageCapacityUnits, pricing.Band{Light: low, Moderate: middle, Heavy: high})
	r.Add(pricing.Component{Name: "Aurora Serverless v2 capacity", Unit: "ACU-hours", UsageBased: true,
		Price:    regional(r, serviceRDS, map[string]string{usagetype: "Aurora:ServerlessV2Usage", "databaseEngine": databaseEngine(r)}),
		Quantity: acu.Mul(pricing.MonthlyHours)})
}

func cacheEngine(r *pricing.Subject) string {
	engine := r.StringOr("engine", cacheEngineDefault)
	if name, ok := cacheEngines[engine]; ok {
		return name
	}
	return engine
}

func replicationGroup(r *pricing.Subject) {
	r.DeclareComputed("node_type")
	r.Add(pricing.Component{Name: "Nodes", Unit: "node-hours",
		Price:    regional(r, serviceElastiCache, map[string]string{usagetype: "NodeUsage:" + r.String("node_type"), "cacheEngine": cacheEngine(r)}),
		Quantity: cacheNodes(r).Mul(pricing.MonthlyHours)})
	billed := func() decimal.Decimal {
		return r.NumberOr("snapshot_retention_limit", decimal.Zero).Sub(decimal.NewFromInt(elastiCacheFreeSnapshots))
	}
	if r.When(func() bool { return billed().IsPositive() }) {
		r.Add(pricing.Component{Name: "Snapshot storage", Unit: "GB-month", UsageBased: true,
			Price: pricing.Query{Vendor: vendor, Service: serviceElastiCache, Region: r.Region(), ProductFamily: "Storage Snapshot",
				Attributes: map[string]string{"cacheEngine": cacheEngine(r)}},
			Quantity: r.Usage(usageSnapshotStorage, storageBand).Mul(billed())})
	}
}

func cacheNodes(r *pricing.Subject) decimal.Decimal {
	if r.Has("num_cache_clusters") || r.Unknown("num_cache_clusters") {
		return r.Number("num_cache_clusters")
	}
	shards := r.NumberOr("num_node_groups", decimal.NewFromInt(1))
	return shards.Mul(r.NumberOr("replicas_per_node_group", decimal.Zero).Add(decimal.NewFromInt(1)))
}

func secret(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Secret", Unit: "secret-months", Quantity: decimal.NewFromInt(1),
		Price: regional(r, serviceSecretsManager, map[string]string{"group": "AWSSecretsManager-Secret"})})
	r.Add(pricing.Component{Name: "API calls", Unit: "requests", UsageBased: true,
		Price:    regional(r, serviceSecretsManager, map[string]string{"group": "AWSSecretsManager-APIRequest"}),
		Quantity: r.Usage(usageRequests, apiCallsBand)})
}

func fargateArchitecture(task *pricing.Subject) string {
	if task.StringOr("runtime_platform.cpu_architecture", fargateX86) == "ARM64" {
		return "ARM-"
	}
	return ""
}

type fargateCapacity struct {
	provider  string
	usagetype string
	vCPU      string
	memory    string
}

var fargateCapacities = []fargateCapacity{
	{provider: fargateLaunchType, usagetype: "Fargate-", vCPU: "vCPU", memory: "Memory"},
	{provider: fargateSpotProvider, usagetype: "Fargate-Spot-", vCPU: "Spot vCPU", memory: "Spot memory"},
}

func fargateService(r *pricing.Subject) {
	for _, capacity := range fargateCapacities {
		if !r.When(func() bool { return fargateTasks(r, capacity.provider).IsPositive() }) {
			continue
		}
		hours := func() decimal.Decimal { return fargateTasks(r, capacity.provider).Mul(pricing.MonthlyHours) }
		task, _ := r.Reference("task_definition")
		architecture := fargateArchitecture(task)
		r.Add(pricing.Component{Name: capacity.vCPU, Unit: "vCPU-hours",
			Price:    regional(r, serviceECS, map[string]string{usagetype: capacity.usagetype + architecture + "vCPU-Hours:perCPU"}),
			Quantity: hours().Mul(task.Quantity("cpu", fargateVCPU))})
		architecture = fargateArchitecture(task)
		r.Add(pricing.Component{Name: capacity.memory, Unit: "GB-hours",
			Price:    regional(r, serviceECS, map[string]string{usagetype: capacity.usagetype + architecture + "GB-Hours"}),
			Quantity: hours().Mul(task.Quantity("memory", fargateGB))})
	}
}

func fargateTasks(r *pricing.Subject, provider string) decimal.Decimal {
	count := r.NumberOr("desired_count", decimal.Zero)
	if launch := r.StringOr("launch_type", ""); launch != "" || r.Unknown("launch_type") {
		if launch == fargateLaunchType && provider == fargateLaunchType {
			return count
		}
		return decimal.Zero
	}
	var strategy []any
	if r.Has(capacityStrategy) {
		strategy = r.List(capacityStrategy)
	}
	if len(strategy) == 0 {
		if !r.Unknown(capacityStrategy) && (count.IsPositive() || r.Unknown("desired_count")) {
			r.MarkUnset("launch_type", "the cluster's default capacity provider strategy decides")
		}
		return decimal.Zero
	}
	var base, weight, ownBase, ownWeight decimal.Decimal
	for i := range strategy {
		at := capacityStrategy + "." + strconv.Itoa(i) + "."
		entryBase, entryWeight := r.NumberOr(at+"base", decimal.Zero), r.NumberOr(at+"weight", decimal.Zero)
		base, weight = base.Add(entryBase), weight.Add(entryWeight)
		if r.String(at+"capacity_provider") == provider {
			ownBase, ownWeight = ownBase.Add(entryBase), ownWeight.Add(entryWeight)
		}
	}
	if weight.IsZero() {
		return ownBase
	}
	return ownBase.Add(decimal.Max(count.Sub(base), decimal.Zero).Mul(ownWeight).Div(weight))
}

func fargateVCPU(text string) (decimal.Decimal, bool) {
	if vCPU, written := strings.CutSuffix(strings.ToLower(strings.TrimSpace(text)), "vcpu"); written {
		return positive(vCPU)
	}
	units, ok := positive(text)
	return units.Div(decimal.NewFromInt(fargateCPUUnits)), ok
}

func fargateGB(text string) (decimal.Decimal, bool) {
	if gb, written := strings.CutSuffix(strings.ToLower(strings.TrimSpace(text)), "gb"); written {
		return positive(gb)
	}
	mib, ok := positive(text)
	return mib.Div(decimal.NewFromInt(fargateMemoryMB)), ok
}

func positive(text string) (decimal.Decimal, bool) {
	parsed, err := decimal.NewFromString(strings.TrimSpace(text))
	return parsed, err == nil && parsed.IsPositive()
}

func loadBalancer(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Application load balancer", Unit: "hours", Quantity: pricing.MonthlyHours,
		Price: regional(r, serviceELB, map[string]string{usagetype: "LoadBalancerUsage", "operation": "LoadBalancing:Application"})})
	lcu := decimal.Max(
		r.Usage(usageNewConnections, connectionBand).Div(decimal.NewFromInt(lcuNewConnectionsPerSecond)),
		r.Usage(usageActiveConnections, activeBand).Div(decimal.NewFromInt(lcuActiveConnectionsPerMinute)),
		r.Usage(usageProcessedBytes, egressBand).Div(pricing.MonthlyHours).Div(decimal.NewFromInt(lcuProcessedGBPerHour)),
		r.Usage(usageRuleEvaluations, ruleBand).Div(decimal.NewFromInt(lcuRuleEvaluationsPerSecond)),
	)
	r.Add(pricing.Component{Name: "Load balancer capacity units", Unit: "LCU-hours", UsageBased: true,
		Price:    regional(r, serviceELB, map[string]string{usagetype: "LCUUsage", "operation": "LoadBalancing:Application"}),
		Quantity: lcu.Mul(pricing.MonthlyHours)})
}

func natGateway(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "NAT gateway", Unit: "hours", Quantity: pricing.MonthlyHours,
		Price: regional(r, serviceEC2, map[string]string{usagetype: "NatGateway-Hours"})})
	r.Add(pricing.Component{Name: "Data processed", Unit: "GB", UsageBased: true,
		Price:    regional(r, serviceEC2, map[string]string{usagetype: "NatGateway-Bytes"}),
		Quantity: r.Usage(usageDataProcessed, egressBand)})
}

func publicAddress(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Public IPv4 address", Unit: "hours", Quantity: pricing.MonthlyHours,
		Price: regional(r, serviceVPC, map[string]string{usagetype: "PublicIPv4:InUseAddress"})})
}

func dynamoTable(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Write request units", Unit: "WRU", UsageBased: true,
		Price:    regional(r, serviceDynamoDB, map[string]string{"group": "DDB-WriteUnits", usagetype: "WriteRequestUnits"}),
		Quantity: r.Usage(usageWriteUnits, tableWriteBand)})
	r.Add(pricing.Component{Name: "Read request units", Unit: "RRU", UsageBased: true,
		Price:    regional(r, serviceDynamoDB, map[string]string{"group": "DDB-ReadUnits", usagetype: "ReadRequestUnits"}),
		Quantity: r.Usage(usageReadUnits, tableReadBand)})
	r.Add(pricing.Component{Name: "Storage", Unit: "GB-month", UsageBased: true, Allowance: pricing.AllowanceAccount,
		Price:    regional(r, serviceDynamoDB, map[string]string{usagetype: "TimedStorage-ByteHrs", "volumeType": "Amazon DynamoDB - Indexed DataStore"}),
		Quantity: r.Usage(usageStorage, tableStoreBand)})
}

func kmsKey(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Customer managed key", Unit: "key-months", Quantity: decimal.NewFromInt(1),
		Price: regional(r, serviceKMS, map[string]string{usagetype: "KMS-Keys"})})
	r.Add(pricing.Component{Name: "Requests", Unit: "requests", UsageBased: true,
		Price:    regional(r, serviceKMS, map[string]string{usagetype: "KMS-Requests"}),
		Quantity: r.Usage(usageRequests, apiCallsBand)})
}

func queue(r *pricing.Subject) {
	queueType := "Standard"
	if r.BoolOr("fifo_queue", false) {
		queueType = "FIFO (first-in, first-out)"
	}
	r.Add(pricing.Component{Name: "Requests", Unit: "requests", UsageBased: true,
		Price:    regional(r, serviceSQS, map[string]string{"group": "SQS-APIRequest-Tier1", "queueType": queueType}),
		Quantity: r.Usage(usageRequests, writesBand)})
}

func topic(r *pricing.Subject) {
	price := regional(r, serviceSNS, map[string]string{usagetype: "Requests-Tier1"})
	allowance := pricing.AllowanceAccount
	if r.BoolOr("fifo_topic", false) {
		price, allowance = regional(r, serviceSNS, map[string]string{usagetype: "F-Request-Tier1"}), ""
	}
	r.Add(pricing.Component{Name: "Publish requests", Unit: "requests", UsageBased: true, Price: price, Allowance: allowance,
		Quantity: r.Usage(usageRequests, writesBand)})
}

func metricAlarm(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Alarm metrics", Unit: "alarm-metrics", Quantity: alarmMetrics(r),
		Price: regional(r, serviceCloudWatch, map[string]string{usagetype: "CW:AlarmMonitorUsage", "alarmType": "Standard"})})
}

func alarmMetrics(r *pricing.Subject) decimal.Decimal {
	if r.StringOr("metric_name", "") != "" {
		return decimal.NewFromInt(1)
	}
	metrics := 0
	for _, query := range r.List("metric_query") {
		entry, _ := query.(map[string]any)
		switch metric := entry["metric"].(type) {
		case map[string]any:
			metrics += min(len(metric), 1)
		case []any:
			metrics += min(len(metric), 1)
		}
	}
	return decimal.NewFromInt(int64(metrics))
}

func schedule(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Invocations", Unit: "invocations", UsageBased: true, Allowance: pricing.AllowanceAccount,
		Price:    regional(r, serviceEvents, map[string]string{usagetype: "ScheduledInvocation"}),
		Quantity: r.Usage(usageInvocations, scheduleBand)})
}

func cloudFrontFunction(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Invocations", Unit: "invocations", UsageBased: true, Allowance: pricing.AllowanceAccount,
		Price:    global(serviceCloudFront, map[string]string{usagetype: "Executions-CloudFrontFunctions"}),
		Quantity: r.Usage(usageInvocations, requestsBand)})
}

func keyValueStore(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Reads", Unit: "reads", UsageBased: true, Allowance: pricing.AllowanceAccount,
		Price:    global(serviceCloudFront, map[string]string{"group": "KVS-Reads", usagetype: "KeyValueStore-EdgeReads"}),
		Quantity: r.Usage(usageStoreReads, requestsBand)})
	r.Add(pricing.Component{Name: "API calls", Unit: "requests", UsageBased: true,
		Price:    global(serviceCloudFront, map[string]string{"group": "KVS-API-Requests", usagetype: "KeyValueStore-APIOperations"}),
		Quantity: r.Usage(usageStoreAPICalls, keyUpdateBand)})
}

func distribution(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "HTTPS requests", Unit: "requests", UsageBased: true,
		Price:    global(serviceCloudFront, map[string]string{usagetype: "US-Requests-Tier2-HTTPS"}),
		Quantity: r.Usage(usageHTTPSRequests, requestsBand)})
	r.Add(pricing.Component{Name: "Data transfer out to internet", Unit: "GB", UsageBased: true,
		Price:    global(serviceCloudFront, map[string]string{usagetype: "US-DataTransfer-Out-Bytes"}),
		Quantity: r.Usage(usageCloudFrontOut, egressBand)})
}

func restAPI(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Requests", Unit: "requests", UsageBased: true,
		Price:    regional(r, serviceAPIGateway, map[string]string{usagetype: "ApiGatewayRequest", "operation": "ApiGatewayRequest"}),
		Quantity: r.Usage(usageRequests, requestsBand)})
}

func eventAPI(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Event API operations", Unit: "operations", UsageBased: true,
		Price:    regional(r, serviceAppSync, map[string]string{usagetype: "EventAPI-Operation"}),
		Quantity: r.Usage(usageEventOperations, requestsBand)})
	r.Add(pricing.Component{Name: "Connection minutes", Unit: "minutes", UsageBased: true,
		Price:    regional(r, serviceAppSync, map[string]string{usagetype: "EventAPI-ConnectionDuration"}),
		Quantity: r.Usage(usageConnectionMinutes, minutesBand)})
}

func registry(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Storage", Unit: "GB-month", UsageBased: true,
		Price:    regional(r, serviceECR, map[string]string{usagetype: "TimedStorage-ByteHrs"}),
		Quantity: r.Usage(usageStorage, imagesBand)})
}

func dataTransfer(r *pricing.Subject) {
	r.Add(pricing.Component{Name: "Data transfer out to internet", Unit: "GB", UsageBased: true,
		Price:    regional(r, serviceDataTransfer, map[string]string{usagetype: "DataTransfer-Out-Bytes", "toLocation": "External"}),
		Quantity: r.Usage(usageOutboundInternet, egressBand)})
}
