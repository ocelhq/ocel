package componenttest

import (
	"reflect"
	"strings"
	"testing"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/apigateway"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/appsync"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudfront"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/dynamodb"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ecr"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ecs"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/elasticache"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/kms"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lambda"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lb"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/rds"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/s3"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/scheduler"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/secretsmanager"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/sns"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/sqs"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ssm"

	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

var syntheticTypes = map[string]bool{"aws_data_transfer": true}

var terraformArguments = map[string]any{
	"aws_api_gateway_rest_api":          apigateway.RestApiArgs{},
	"aws_appsync_api":                   appsync.ApiArgs{},
	"aws_cloudfront_distribution":       cloudfront.DistributionArgs{},
	"aws_cloudfront_function":           cloudfront.FunctionArgs{},
	"aws_cloudfront_key_value_store":    cloudfront.KeyValueStoreArgs{},
	"aws_cloudwatch_log_group":          cloudwatch.LogGroupArgs{},
	"aws_cloudwatch_metric_alarm":       cloudwatch.MetricAlarmArgs{},
	"aws_dynamodb_table":                dynamodb.TableArgs{},
	"aws_ecr_repository":                ecr.RepositoryArgs{},
	"aws_ecs_cluster":                   ecs.ClusterArgs{},
	"aws_ecs_service":                   ecs.ServiceArgs{},
	"aws_ecs_task_definition":           ecs.TaskDefinitionArgs{},
	"aws_eip":                           ec2.EipArgs{},
	"aws_elasticache_parameter_group":   elasticache.ParameterGroupArgs{},
	"aws_elasticache_replication_group": elasticache.ReplicationGroupArgs{},
	"aws_elasticache_subnet_group":      elasticache.SubnetGroupArgs{},
	"aws_internet_gateway":              ec2.InternetGatewayArgs{},
	"aws_kms_key":                       kms.KeyArgs{},
	"aws_lambda_event_source_mapping":   lambda.EventSourceMappingArgs{},
	"aws_lambda_function":               lambda.FunctionArgs{},
	"aws_lambda_function_url":           lambda.FunctionUrlArgs{},
	"aws_lambda_layer_version":          lambda.LayerVersionArgs{},
	"aws_lb":                            lb.LoadBalancerArgs{},
	"aws_nat_gateway":                   ec2.NatGatewayArgs{},
	"aws_rds_cluster":                   rds.ClusterArgs{},
	"aws_rds_cluster_instance":          rds.ClusterInstanceArgs{},
	"aws_s3_bucket":                     s3.BucketArgs{},
	"aws_scheduler_schedule":            scheduler.ScheduleArgs{},
	"aws_secretsmanager_secret":         secretsmanager.SecretArgs{},
	"aws_sns_topic":                     sns.TopicArgs{},
	"aws_sqs_queue":                     sqs.QueueArgs{},
	"aws_ssm_parameter":                 ssm.ParameterArgs{},
	"aws_subnet":                        ec2.SubnetArgs{},
	"aws_vpc":                           ec2.VpcArgs{},
	"aws_vpc_endpoint":                  ec2.VpcEndpointArgs{},
}

var pluralBlocks = map[string]string{"metric_query": "MetricQueries"}

func pulumiField(argument string) string {
	if field, plural := pluralBlocks[argument]; plural {
		return field
	}
	var b strings.Builder
	for word := range strings.SplitSeq(argument, "_") {
		b.WriteString(strings.ToUpper(word[:1]) + word[1:])
	}
	return b.String()
}

func RefuseInventedArguments(t testing.TB, set *costv1.ResourceSet) {
	t.Helper()
	for _, listed := range set.GetResources() {
		if listed.GetVendor() != "aws" || syntheticTypes[listed.GetType()] {
			continue
		}
		args, known := terraformArguments[listed.GetType()]
		if !known {
			t.Errorf("%s is a type componenttest holds no Terraform arguments for", listed.GetType())
			continue
		}
		schema := reflect.TypeOf(args)
		arguments := append(listed.GetUnknown(), listedKeys(listed)...)
		for _, argument := range arguments {
			top, _, _ := strings.Cut(argument, ".")
			if _, ok := schema.FieldByName(pulumiField(top)); !ok {
				t.Errorf("%s lists %q, which is no argument of the Terraform resource", listed.GetId(), argument)
			}
		}
	}
}

func listedKeys(listed *costv1.Resource) []string {
	var keys []string
	for key := range listed.GetProperties().GetFields() {
		keys = append(keys, key)
	}
	for key := range listed.GetReferences() {
		keys = append(keys, key)
	}
	return keys
}
