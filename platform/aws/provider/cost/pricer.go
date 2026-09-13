package cost

import (
	_ "embed"
	"sync"

	"github.com/shopspring/decimal"

	"github.com/ocelhq/ocel/pkg/costkit"
	costv1 "github.com/ocelhq/ocel/pkg/proto/provider/cost/v1"
)

const Vendor = "aws"

//go:embed rates.json
var rates []byte

var Card = sync.OnceValues(func() (*costkit.Card, error) { return costkit.Load(rates) })

var Notes = []string{
	"list prices for us-east-1; a resource in another region is left unpriced",
	"an always-free allowance the price list folds into a first tier is spent once per account across every resource sharing it; allowances the price list leaves out, such as Lambda's, are not applied",
	"CloudFront is priced at its United States rates whichever price class the distribution carries",
}

const (
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

	endpointGateway = "Gateway"
	endpointType    = "vpc_endpoint_type"
	endpointSubnets = "subnet_ids"

	lambdaGB        = 1024
	fargateCPUUnits = 1024
	fargateMemoryMB = 1024
	secondsPerHour  = 3600

	lcuNewConnectionsPerSecond    = 25
	lcuActiveConnectionsPerMinute = 3000
	lcuProcessedGBPerHour         = 1
	lcuRuleEvaluationsPerSecond   = 1000
)

var (
	requestsBand   = costkit.Band{Light: 100_000, Moderate: 1_000_000, Heavy: 10_000_000}
	durationBand   = costkit.Band{Light: 100, Moderate: 200, Heavy: 500}
	storageBand    = costkit.Band{Light: 1, Moderate: 10, Heavy: 100}
	logsBand       = costkit.Band{Light: 0.1, Moderate: 1, Heavy: 10}
	logStorageBand = costkit.Band{Light: 0.05, Moderate: 0.5, Heavy: 5}
	writesBand     = costkit.Band{Light: 1_000, Moderate: 10_000, Heavy: 100_000}
	readsBand      = costkit.Band{Light: 10_000, Moderate: 100_000, Heavy: 1_000_000}
	writeRateBand  = costkit.Band{Light: 0.1, Moderate: 1, Heavy: 10}
	readRateBand   = costkit.Band{Light: 0.3, Moderate: 3, Heavy: 30}
	connectionBand = costkit.Band{Light: 1, Moderate: 10, Heavy: 100}
	activeBand     = costkit.Band{Light: 100, Moderate: 1_000, Heavy: 10_000}
	ruleBand       = costkit.Band{Light: 1, Moderate: 10, Heavy: 100}
	egressBand     = costkit.Band{Light: 1, Moderate: 10, Heavy: 100}
	tableWriteBand = costkit.Band{Light: 10_000, Moderate: 100_000, Heavy: 1_000_000}
	tableReadBand  = costkit.Band{Light: 10_000, Moderate: 100_000, Heavy: 1_000_000}
	tableStoreBand = costkit.Band{Light: 0.1, Moderate: 1, Heavy: 10}
	apiCallsBand   = costkit.Band{Light: 1_000, Moderate: 10_000, Heavy: 100_000}
	imagesBand     = costkit.Band{Light: 1, Moderate: 5, Heavy: 50}
	natBand        = costkit.Band{Light: 10, Moderate: 100, Heavy: 1_000}
	endpointBand   = costkit.Band{Light: 1, Moderate: 10, Heavy: 100}

	thousand        = decimal.NewFromInt(1000)
	secondsPerMonth = costkit.MonthlyHours.Mul(decimal.NewFromInt(secondsPerHour))
)

var Table = costkit.Table{
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
	"aws_data_transfer":               dataTransfer,
	"aws_vpc":                         free,
	"aws_subnet":                      free,
	"aws_security_group":              free,
	"aws_route_table":                 free,
	"aws_internet_gateway":            free,
	"aws_nat_gateway":                 natGateway,
	"aws_eip":                         elasticIP,
	"aws_vpc_endpoint":                vpcEndpoint,
}

func Price(req *costv1.PriceRequest, edges ...costkit.EdgePricer) (*costv1.Estimate, error) {
	held, err := Card()
	if err != nil {
		return nil, err
	}
	merged, pricing, err := costkit.Priced(held, Table, edges...)
	if err != nil {
		return nil, err
	}
	estimate, err := costkit.Estimate(merged, pricing, req)
	if err != nil {
		return nil, err
	}
	estimate.Notes = append(estimate.Notes, Notes...)
	return estimate, nil
}

func free(r *costkit.Subject) { r.Free() }

func lambdaRates(r *costkit.Subject) (requests, duration string) {
	if archs := r.List("architectures"); len(archs) > 0 && archs[0] == "arm64" {
		return "aws/lambda/requests-arm", "aws/lambda/duration-arm"
	}
	return "aws/lambda/requests", "aws/lambda/duration"
}

func lambdaFunction(r *costkit.Subject) {
	requests, _ := lambdaRates(r)
	r.Add(costkit.Component{Name: "Requests", Unit: "requests", Rate: requests, Quantity: r.Usage(usageRequests, requestsBand), UsageBased: true})
	_, duration := lambdaRates(r)
	seconds := r.Usage(usageRequests, requestsBand).Mul(r.Usage(usageRequestDuration, durationBand)).Div(thousand)
	r.Add(costkit.Component{
		Name: "Duration", Unit: "GB-seconds", Rate: duration, UsageBased: true,
		Quantity: seconds.Mul(r.Number("memory_size")).Div(decimal.NewFromInt(lambdaGB)),
	})
}

func logGroup(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Ingested", Unit: "GB", Rate: "aws/logs/ingest", Quantity: r.Usage(usageLogsIngested, logsBand), UsageBased: true})
	r.Add(costkit.Component{Name: "Stored", Unit: "GB-month", Rate: "aws/logs/storage", Quantity: r.Usage(usageStorage, logStorageBand), UsageBased: true})
}

func bucket(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Storage", Unit: "GB-month", Rate: "aws/s3/storage", Quantity: r.Usage(usageS3Storage, storageBand), UsageBased: true})
	r.Add(costkit.Component{Name: "PUT, COPY, POST, LIST requests", Unit: "requests", Rate: "aws/s3/requests-tier1", Quantity: r.Usage(usageS3Tier1Requests, writesBand), UsageBased: true})
	r.Add(costkit.Component{Name: "GET and other requests", Unit: "requests", Rate: "aws/s3/requests-tier2", Quantity: r.Usage(usageS3Tier2Requests, readsBand), UsageBased: true})
}

func auroraCluster(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Storage", Unit: "GB-month", Rate: "aws/rds/aurora-storage", Quantity: r.Usage(usageStorage, storageBand), UsageBased: true})
	perSecond := r.Usage(usageWritesPerSecond, writeRateBand).Add(r.Usage(usageReadsPerSecond, readRateBand))
	r.Add(costkit.Component{Name: "I/O requests", Unit: "requests", Rate: "aws/rds/aurora-io", Quantity: perSecond.Mul(secondsPerMonth), UsageBased: true})
}

func auroraInstance(r *costkit.Subject) {
	if class := r.String("instance_class"); class != "db.serverless" {
		r.Add(costkit.Component{Name: "Instance", Unit: "hours", Rate: "aws/rds/instance-" + class, Quantity: costkit.MonthlyHours})
		return
	}
	minimum := r.Number("serverlessv2_scaling_configuration.min_capacity")
	maximum := r.Number("serverlessv2_scaling_configuration.max_capacity")
	middle, _ := minimum.Add(maximum).Div(decimal.NewFromInt(2)).Float64()
	low, _ := minimum.Float64()
	high, _ := maximum.Float64()
	acu := r.Usage(usageCapacityUnits, costkit.Band{Light: low, Moderate: middle, Heavy: high})
	r.Add(costkit.Component{Name: "Aurora Serverless v2 capacity", Unit: "ACU-hours", Rate: "aws/rds/aurora-serverless-v2", Quantity: acu.Mul(costkit.MonthlyHours), UsageBased: true})
}

func secret(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Secret", Unit: "secret-months", Rate: "aws/secretsmanager/secret", Quantity: decimal.NewFromInt(1)})
	r.Add(costkit.Component{Name: "API calls", Unit: "requests", Rate: "aws/secretsmanager/requests", Quantity: r.Usage(usageRequests, apiCallsBand), UsageBased: true})
}

func fargateRates(r *costkit.Subject) (vcpu, memory string) {
	if r.String("runtime_platform.cpu_architecture") == "ARM64" {
		return "aws/fargate/vcpu-arm", "aws/fargate/memory-arm"
	}
	return "aws/fargate/vcpu", "aws/fargate/memory"
}

func fargateService(r *costkit.Subject) {
	hours := func() decimal.Decimal { return r.Number("desired_count").Mul(costkit.MonthlyHours) }
	vcpu, _ := fargateRates(r)
	r.Add(costkit.Component{Name: "vCPU", Unit: "vCPU-hours", Rate: vcpu, Quantity: hours().Mul(r.Number("cpu")).Div(decimal.NewFromInt(fargateCPUUnits))})
	_, memory := fargateRates(r)
	r.Add(costkit.Component{Name: "Memory", Unit: "GB-hours", Rate: memory, Quantity: hours().Mul(r.Number("memory")).Div(decimal.NewFromInt(fargateMemoryMB))})
}

func loadBalancer(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Application load balancer", Unit: "hours", Rate: "aws/alb/hours", Quantity: costkit.MonthlyHours})
	lcu := decimal.Max(
		r.Usage(usageNewConnections, connectionBand).Div(decimal.NewFromInt(lcuNewConnectionsPerSecond)),
		r.Usage(usageActiveConnections, activeBand).Div(decimal.NewFromInt(lcuActiveConnectionsPerMinute)),
		r.Usage(usageProcessedBytes, egressBand).Div(costkit.MonthlyHours).Div(decimal.NewFromInt(lcuProcessedGBPerHour)),
		r.Usage(usageRuleEvaluations, ruleBand).Div(decimal.NewFromInt(lcuRuleEvaluationsPerSecond)),
	)
	r.Add(costkit.Component{Name: "Load balancer capacity units", Unit: "LCU-hours", Rate: "aws/alb/lcu", Quantity: lcu.Mul(costkit.MonthlyHours), UsageBased: true})
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
	r.Add(costkit.Component{Name: "HTTPS requests", Unit: "requests", Rate: "aws/cloudfront/requests-https", Quantity: r.Usage(usageHTTPSRequests, requestsBand), UsageBased: true})
	r.Add(costkit.Component{Name: "Data transfer out to internet", Unit: "GB", Rate: "aws/cloudfront/data-out", Quantity: r.Usage(usageCloudFrontOut, egressBand), UsageBased: true})
}

func restAPI(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Requests", Unit: "requests", Rate: "aws/apigateway/rest-requests", Quantity: r.Usage(usageRequests, requestsBand), UsageBased: true})
}

func registry(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Storage", Unit: "GB-month", Rate: "aws/ecr/storage", Quantity: r.Usage(usageStorage, imagesBand), UsageBased: true})
}

func natGateway(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "NAT gateway", Unit: "hours", Rate: "aws/vpc/nat-gateway-hours", Quantity: costkit.MonthlyHours})
	r.Add(costkit.Component{Name: "Data processed", Unit: "GB", Rate: "aws/vpc/nat-gateway-data", Quantity: r.Usage(usageProcessedBytes, natBand), UsageBased: true})
}

func elasticIP(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Public IPv4 address", Unit: "hours", Rate: "aws/vpc/public-ipv4", Quantity: costkit.MonthlyHours})
}

func vpcEndpoint(r *costkit.Subject) {
	if r.String(endpointType) == endpointGateway {
		r.Free()
		return
	}
	zones := decimal.NewFromInt(int64(len(r.List(endpointSubnets))))
	if zones.IsZero() {
		zones = decimal.NewFromInt(1)
	}
	r.Add(costkit.Component{Name: "Interface endpoint", Unit: "hours", Rate: "aws/vpc/endpoint-hours", Quantity: zones.Mul(costkit.MonthlyHours), Needs: []string{endpointType, endpointSubnets}})
	r.Add(costkit.Component{Name: "Data processed", Unit: "GB", Rate: "aws/vpc/endpoint-data", Quantity: r.Usage(usageProcessedBytes, endpointBand), UsageBased: true})
}

func dataTransfer(r *costkit.Subject) {
	r.Add(costkit.Component{Name: "Data transfer out to internet", Unit: "GB", Rate: "aws/datatransfer/out", Quantity: r.Usage(usageOutboundInternet, egressBand), UsageBased: true})
}
