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
	usageRequestDuration = "request_duration_ms"
	usageStorage         = "storage_gb"
	usageLogsIngested    = "monthly_data_ingested_gb"
	usageTier1Requests   = "monthly_tier_1_requests"
	usageTier2Requests   = "monthly_tier_2_requests"
	usageCapacityUnits   = "capacity_units_per_hr"
	usageIORequests      = "monthly_io_requests"
	usageLCUHours        = "monthly_lcu_hours"
	usageWriteUnits      = "monthly_write_request_units"
	usageReadUnits       = "monthly_read_request_units"
	usageDataOut         = "monthly_data_transfer_to_internet_gb"
	usageInvocations     = "monthly_invocations"
	usageAPICalls        = "monthly_api_calls"

	lambdaGB        = 1024
	fargateCPUUnits = 1024
	fargateMemoryMB = 1024
)

var (
	requestsBand   = costkit.Band{Light: 100_000, Moderate: 1_000_000, Heavy: 10_000_000}
	durationBand   = costkit.Band{Light: 100, Moderate: 200, Heavy: 500}
	storageBand    = costkit.Band{Light: 1, Moderate: 10, Heavy: 100}
	logsBand       = costkit.Band{Light: 0.1, Moderate: 1, Heavy: 10}
	logStorageBand = costkit.Band{Light: 0.05, Moderate: 0.5, Heavy: 5}
	writesBand     = costkit.Band{Light: 1_000, Moderate: 10_000, Heavy: 100_000}
	readsBand      = costkit.Band{Light: 10_000, Moderate: 100_000, Heavy: 1_000_000}
	ioBand         = costkit.Band{Light: 1_000_000, Moderate: 10_000_000, Heavy: 100_000_000}
	lcuBand        = costkit.Band{Light: 0, Moderate: 50, Heavy: 730}
	egressBand     = costkit.Band{Light: 1, Moderate: 10, Heavy: 100}
	tableWriteBand = costkit.Band{Light: 10_000, Moderate: 100_000, Heavy: 1_000_000}
	tableReadBand  = costkit.Band{Light: 10_000, Moderate: 100_000, Heavy: 1_000_000}
	tableStoreBand = costkit.Band{Light: 0.1, Moderate: 1, Heavy: 10}
	apiCallsBand   = costkit.Band{Light: 1_000, Moderate: 10_000, Heavy: 100_000}
	imagesBand     = costkit.Band{Light: 1, Moderate: 5, Heavy: 50}
)

var thousand = decimal.NewFromInt(1000)

var table = costkit.Table{
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
	"aws_dynamodb_table":              dynamoTable,
	"aws_kms_key":                     kmsKey,
	"aws_sqs_queue":                   queue,
	"aws_cloudfront_function":         cloudFrontFunction,
	"aws_cloudfront_key_value_store":  free,
	"aws_cloudfront_distribution":     distribution,
	"aws_api_gateway_rest_api":        restAPI,
	"aws_ecr_repository":              registry,
}

func Price(req *costv1.PriceRequest) (*costv1.Estimate, error) {
	held, err := card()
	if err != nil {
		return nil, err
	}
	estimate, err := costkit.Estimate(held, table, req)
	if err != nil {
		return nil, err
	}
	estimate.Notes = append(estimate.Notes,
		"list prices for us-east-1; a resource in another region is left unpriced",
		"always-free allowances are not applied, except where the price list folds one into the first tier",
		"CloudFront is priced at its United States rates whichever price class the distribution carries",
	)
	return estimate, nil
}

func free(r *costkit.Subject) { r.Free() }

func arm(r *costkit.Subject) bool {
	archs, _ := r.Resource.GetProperties().AsMap()["architectures"].([]any)
	return len(archs) > 0 && archs[0] == "arm64"
}

func lambdaFunction(r *costkit.Subject) {
	requests, duration := "aws/lambda/requests", "aws/lambda/duration"
	if arm(r) {
		requests, duration = "aws/lambda/requests-arm", "aws/lambda/duration-arm"
	}
	monthly := r.Usage(usageRequests, requestsBand)
	r.Add(costkit.Component{Name: "Requests", Unit: "requests", Rate: requests, Quantity: monthly, UsageBased: true})
	memory := r.Number("memory_size")
	seconds := r.Usage(usageRequestDuration, durationBand).Div(thousand)
	r.Add(costkit.Component{
		Name: "Duration", Unit: "GB-seconds", Rate: duration, UsageBased: true,
		Quantity: monthly.Mul(seconds).Mul(memory).Div(decimal.NewFromInt(lambdaGB)),
	})
}

func logGroup(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Ingested", Unit: "GB", Rate: "aws/logs/ingest", Quantity: r.Usage(usageLogsIngested, logsBand), UsageBased: true})
	r.Add(costkit.Component{Name: "Stored", Unit: "GB-month", Rate: "aws/logs/storage", Quantity: r.Usage(usageStorage, logStorageBand), UsageBased: true})
}

func bucket(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Storage", Unit: "GB-month", Rate: "aws/s3/storage", Quantity: r.Usage(usageStorage, storageBand), UsageBased: true})
	r.Add(costkit.Component{Name: "PUT, COPY, POST, LIST requests", Unit: "requests", Rate: "aws/s3/requests-tier1", Quantity: r.Usage(usageTier1Requests, writesBand), UsageBased: true})
	r.Add(costkit.Component{Name: "GET and other requests", Unit: "requests", Rate: "aws/s3/requests-tier2", Quantity: r.Usage(usageTier2Requests, readsBand), UsageBased: true})
}

func auroraCluster(r *costkit.Subject) {
	minimum := r.Number("serverlessv2_scaling_configuration.min_capacity")
	maximum := r.Number("serverlessv2_scaling_configuration.max_capacity")
	middle, _ := minimum.Add(maximum).Div(decimal.NewFromInt(2)).Float64()
	low, _ := minimum.Float64()
	high, _ := maximum.Float64()
	acu := r.Usage(usageCapacityUnits, costkit.Band{Light: low, Moderate: middle, Heavy: high})
	r.Add(costkit.Component{Name: "Aurora Serverless v2 capacity", Unit: "ACU-hours", Rate: "aws/rds/aurora-serverless-v2", Quantity: acu.Mul(costkit.MonthlyHours), UsageBased: true})
	r.Add(costkit.Component{Name: "Storage", Unit: "GB-month", Rate: "aws/rds/aurora-storage", Quantity: r.Usage(usageStorage, storageBand), UsageBased: true})
	r.Add(costkit.Component{Name: "I/O requests", Unit: "requests", Rate: "aws/rds/aurora-io", Quantity: r.Usage(usageIORequests, ioBand), UsageBased: true})
}

func auroraInstance(r *costkit.Subject) {
	if r.String("instance_class") == "db.serverless" {
		r.Free()
	}
}

func secret(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Secret", Unit: "secret-months", Rate: "aws/secretsmanager/secret", Quantity: decimal.NewFromInt(1)})
	r.Add(costkit.Component{Name: "API calls", Unit: "requests", Rate: "aws/secretsmanager/requests", Quantity: r.Usage(usageAPICalls, apiCallsBand), UsageBased: true})
}

func fargateService(r *costkit.Subject) {
	vcpu, memory := "aws/fargate/vcpu", "aws/fargate/memory"
	if r.String("runtime_platform.cpu_architecture") == "ARM64" {
		vcpu, memory = "aws/fargate/vcpu-arm", "aws/fargate/memory-arm"
	}
	tasks := r.Number("desired_count")
	hours := tasks.Mul(costkit.MonthlyHours)
	r.Add(costkit.Component{Name: "vCPU", Unit: "vCPU-hours", Rate: vcpu, Quantity: hours.Mul(r.Number("cpu")).Div(decimal.NewFromInt(fargateCPUUnits))})
	r.Add(costkit.Component{Name: "Memory", Unit: "GB-hours", Rate: memory, Quantity: hours.Mul(r.Number("memory")).Div(decimal.NewFromInt(fargateMemoryMB))})
}

func loadBalancer(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Application load balancer", Unit: "hours", Rate: "aws/alb/hours", Quantity: costkit.MonthlyHours})
	r.Add(costkit.Component{Name: "Load balancer capacity units", Unit: "LCU-hours", Rate: "aws/alb/lcu", Quantity: r.Usage(usageLCUHours, lcuBand), UsageBased: true})
}

func dynamoTable(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Write request units", Unit: "WRU", Rate: "aws/dynamodb/write", Quantity: r.Usage(usageWriteUnits, tableWriteBand), UsageBased: true})
	r.Add(costkit.Component{Name: "Read request units", Unit: "RRU", Rate: "aws/dynamodb/read", Quantity: r.Usage(usageReadUnits, tableReadBand), UsageBased: true})
	r.Add(costkit.Component{Name: "Storage", Unit: "GB-month", Rate: "aws/dynamodb/storage", Quantity: r.Usage(usageStorage, tableStoreBand), UsageBased: true})
}

func kmsKey(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Customer managed key", Unit: "key-months", Rate: "aws/kms/key", Quantity: decimal.NewFromInt(1)})
	r.Add(costkit.Component{Name: "Requests", Unit: "requests", Rate: "aws/kms/requests", Quantity: r.Usage(usageRequests, apiCallsBand), UsageBased: true})
}

func queue(r *costkit.Subject) {
	rate := "aws/sqs/requests"
	if r.Bool("fifo_queue") {
		rate = "aws/sqs/requests-fifo"
	}
	r.Add(costkit.Component{Name: "Requests", Unit: "requests", Rate: rate, Quantity: r.Usage(usageRequests, writesBand), UsageBased: true})
}

func cloudFrontFunction(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Invocations", Unit: "invocations", Rate: "aws/cloudfront/functions", Quantity: r.Usage(usageInvocations, requestsBand), UsageBased: true})
}

func distribution(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "HTTPS requests", Unit: "requests", Rate: "aws/cloudfront/requests-https", Quantity: r.Usage(usageRequests, requestsBand), UsageBased: true})
	r.Add(costkit.Component{Name: "Data transfer out to internet", Unit: "GB", Rate: "aws/cloudfront/data-out", Quantity: r.Usage(usageDataOut, egressBand), UsageBased: true})
}

func restAPI(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Requests", Unit: "requests", Rate: "aws/apigateway/rest-requests", Quantity: r.Usage(usageRequests, requestsBand), UsageBased: true})
	r.Add(costkit.Component{Name: "Data transfer out to internet", Unit: "GB", Rate: "aws/datatransfer/out", Quantity: r.Usage(usageDataOut, egressBand), UsageBased: true})
}

func registry(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Storage", Unit: "GB-month", Rate: "aws/ecr/storage", Quantity: r.Usage(usageStorage, imagesBand), UsageBased: true})
}
