package pricing_test

import (
	"maps"
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/costkit"
	"github.com/ocelhq/ocel/pkg/costkit/pulumi"
	aws "github.com/ocelhq/ocel/platform/aws/provider/cost"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy/cost"
	gcp "github.com/ocelhq/ocel/platform/gcp/provider/cost"
)

const declaredByNoTool = "aws_data_transfer"

var pricedTokens = map[string]string{
	"aws aws_api_gateway_rest_api":                         "aws:apigateway/restApi:RestApi",
	"aws aws_cloudfront_distribution":                      "aws:cloudfront/distribution:Distribution",
	"aws aws_cloudfront_function":                          "aws:cloudfront/function:Function",
	"aws aws_cloudfront_key_value_store":                   "aws:cloudfront/keyValueStore:KeyValueStore",
	"aws aws_cloudwatch_log_group":                         "aws:cloudwatch/logGroup:LogGroup",
	"aws aws_dynamodb_table":                               "aws:dynamodb/table:Table",
	"aws aws_ecr_repository":                               "aws:ecr/repository:Repository",
	"aws aws_ecs_cluster":                                  "aws:ecs/cluster:Cluster",
	"aws aws_ecs_service":                                  "aws:ecs/service:Service",
	"aws aws_ecs_task_definition":                          "aws:ecs/taskDefinition:TaskDefinition",
	"aws aws_eip":                                          "aws:ec2/eip:Eip",
	"aws aws_internet_gateway":                             "aws:ec2/internetGateway:InternetGateway",
	"aws aws_kms_key":                                      "aws:kms/key:Key",
	"aws aws_lambda_event_source_mapping":                  "aws:lambda/eventSourceMapping:EventSourceMapping",
	"aws aws_lambda_function":                              "aws:lambda/function:Function",
	"aws aws_lambda_function_url":                          "aws:lambda/functionUrl:FunctionUrl",
	"aws aws_lambda_layer_version":                         "aws:lambda/layerVersion:LayerVersion",
	"aws aws_lb":                                           "aws:lb/loadBalancer:LoadBalancer",
	"aws aws_nat_gateway":                                  "aws:ec2/natGateway:NatGateway",
	"aws aws_rds_cluster":                                  "aws:rds/cluster:Cluster",
	"aws aws_rds_cluster_instance":                         "aws:rds/clusterInstance:ClusterInstance",
	"aws aws_route_table":                                  "aws:ec2/routeTable:RouteTable",
	"aws aws_s3_bucket":                                    "aws:s3/bucket:Bucket",
	"aws aws_secretsmanager_secret":                        "aws:secretsmanager/secret:Secret",
	"aws aws_security_group":                               "aws:ec2/securityGroup:SecurityGroup",
	"aws aws_sqs_queue":                                    "aws:sqs/queue:Queue",
	"aws aws_subnet":                                       "aws:ec2/subnet:Subnet",
	"aws aws_vpc":                                          "aws:ec2/vpc:Vpc",
	"aws aws_vpc_endpoint":                                 "aws:ec2/vpcEndpoint:VpcEndpoint",
	"cloudflare cloudflare_account_subscription":           "cloudflare:index/accountSubscription:AccountSubscription",
	"cloudflare cloudflare_r2_bucket":                      "cloudflare:index/r2Bucket:R2Bucket",
	"cloudflare cloudflare_workers_script":                 "cloudflare:index/workersScript:WorkersScript",
	"gcp google_artifact_registry_repository":              "gcp:artifactregistry/repository:Repository",
	"gcp google_certificate_manager_certificate_map":       "gcp:certificatemanager/certificateMap:CertificateMap",
	"gcp google_certificate_manager_certificate_map_entry": "gcp:certificatemanager/certificateMapEntry:CertificateMapEntry",
	"gcp google_cloud_run_v2_service":                      "gcp:cloudrunv2/service:Service",
	"gcp google_compute_backend_service":                   "gcp:compute/backendService:BackendService",
	"gcp google_compute_global_address":                    "gcp:compute/globalAddress:GlobalAddress",
	"gcp google_compute_global_forwarding_rule":            "gcp:compute/globalForwardingRule:GlobalForwardingRule",
	"gcp google_compute_region_network_endpoint_group":     "gcp:compute/regionNetworkEndpointGroup:RegionNetworkEndpointGroup",
	"gcp google_compute_target_https_proxy":                "gcp:compute/targetHttpsProxy:TargetHttpsProxy",
	"gcp google_compute_url_map":                           "gcp:compute/uRLMap:URLMap",
	"gcp google_firestore_database":                        "gcp:firestore/database:Database",
	"gcp google_kms_crypto_key":                            "gcp:kms/cryptoKey:CryptoKey",
	"gcp google_kms_key_ring":                              "gcp:kms/keyRing:KeyRing",
	"gcp google_secret_manager_secret":                     "gcp:secretmanager/secret:Secret",
	"gcp google_service_account":                           "gcp:serviceaccount/account:Account",
	"gcp google_storage_bucket":                            "gcp:storage/bucket:Bucket",
}

type vendorTable struct {
	name  string
	table costkit.Table
}

func vendorTables() []vendorTable {
	return []vendorTable{{aws.Vendor, aws.Table}, {gcp.Vendor, gcp.Table}, {cloudflare.Vendor, cloudflare.Table}}
}

func TestTheForeignSourcesKnowEveryTypeThisServicePrices(t *testing.T) {
	var want []string
	var exempt []string
	for _, vendor := range vendorTables() {
		for _, typ := range slices.Sorted(maps.Keys(vendor.table)) {
			if typ == declaredByNoTool {
				exempt = append(exempt, vendor.name+" "+typ)
				continue
			}
			want = append(want, vendor.name+" "+typ)
		}
	}
	slices.Sort(want)
	if len(exempt) != 1 {
		t.Errorf("%v claim the %s exemption, want the one type no tool declares", exempt, declaredByNoTool)
	}

	for _, priced := range want {
		typ, recorded := pricedTokens[priced]
		if !recorded {
			t.Errorf("%s is priced and no pulumi token here maps to it", priced)
			continue
		}
		vendor, tf, ok := pulumi.Token(typ)
		if !ok || vendor+" "+tf != priced {
			t.Errorf("Token(%q) = %q %q %v, want %q", typ, vendor, tf, ok, priced)
		}
	}
	for _, priced := range slices.Sorted(maps.Keys(pricedTokens)) {
		if !slices.Contains(want, priced) {
			t.Errorf("%q is recorded here and nothing prices it", priced)
		}
	}
}

func TestTheParserSpeaksForEveryVendorThisServicePrices(t *testing.T) {
	var want []string
	for _, vendor := range vendorTables() {
		want = append(want, vendor.name)
	}
	slices.Sort(want)

	if got := pulumi.Vendors(); !slices.Equal(got, want) {
		t.Errorf("Vendors() = %v, want %v", got, want)
	}
}
