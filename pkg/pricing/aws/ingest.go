package aws

import "github.com/ocelhq/ocel/pkg/pricing/ingest"

const (
	serviceAPIGateway     = "AmazonApiGateway"
	serviceAppSync        = "AWSAppSync"
	serviceCloudFront     = "AmazonCloudFront"
	serviceCloudWatch     = "AmazonCloudWatch"
	serviceDataTransfer   = "AWSDataTransfer"
	serviceDynamoDB       = "AmazonDynamoDB"
	serviceEC2            = "AmazonEC2"
	serviceECR            = "AmazonECR"
	serviceECS            = "AmazonECS"
	serviceELB            = "AWSELB"
	serviceElastiCache    = "AmazonElastiCache"
	serviceEvents         = "AWSEvents"
	serviceKMS            = "awskms"
	serviceLambda         = "AWSLambda"
	serviceRDS            = "AmazonRDS"
	serviceS3             = "AmazonS3"
	serviceSecretsManager = "AWSSecretsManager"
	serviceSNS            = "AmazonSNS"
	serviceSQS            = "AWSQueueService"
	serviceVPC            = "AmazonVPC"
)

var edgeZones = []string{"AWS Local Zone", "AWS Wavelength Zone"}

func service(name string, families ...string) ingest.Service {
	return ingest.Service{
		Service:         name,
		ProductFamilies: families,
		Exclude: []ingest.Filter{
			{Attribute: "locationType", In: edgeZones},
			{Attribute: "fromLocationType", In: edgeZones},
			{Attribute: "toLocationType", In: edgeZones},
		},
	}
}

var ingestion = ingest.Vendor{
	Vendor: vendor,
	Source: ingest.SourceAWSOffers,
	Services: []ingest.Service{
		service(serviceAPIGateway, "API Calls"),
		service(serviceAppSync, "RealTime"),
		service(serviceCloudFront, "Data Transfer", "Request"),
		service(serviceCloudWatch, "Alarm", "Data Payload", "Storage Snapshot"),
		service(serviceDataTransfer, "Data Transfer"),
		service(serviceDynamoDB, "Amazon DynamoDB PayPerRequest Throughput", "Database Storage"),
		service(serviceEC2, "NAT Gateway"),
		service(serviceECR, "EC2 Container Registry"),
		service(serviceECS, "Compute"),
		service(serviceELB, "Load Balancer-Application"),
		service(serviceElastiCache, "Cache Instance", "Storage Snapshot"),
		service(serviceEvents, "EventBridge"),
		service(serviceKMS, "API Request", "Encryption Key"),
		service(serviceLambda, "Serverless"),
		service(serviceRDS, "Database Instance", "Database Storage", "ServerlessV2", "System Operation"),
		service(serviceS3, "API Request", "Storage"),
		service(serviceSecretsManager, "API Request", "Secret"),
		service(serviceSNS),
		service(serviceSQS, "API Request"),
		service(serviceVPC),
	},
}
