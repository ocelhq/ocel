package aws_test

import (
	"google.golang.org/protobuf/types/known/structpb"

	costv1 "github.com/ocelhq/ocel/pkg/proto/common/cost/v1"
)

var fixtureRegions = []string{"us-east-1", "eu-west-2", "eu-west-1"}

func resource(id, typ, region string, props map[string]any) *costv1.Resource {
	properties, err := structpb.NewStruct(props)
	if err != nil {
		panic(err)
	}
	return &costv1.Resource{Id: id, Scope: "stack", Vendor: "aws", Type: typ, Name: id, Region: region, Properties: properties}
}

func referencing(r *costv1.Resource, references map[string]string) *costv1.Resource {
	r.References = references
	return r
}

func inventory(region string) *costv1.ResourceSet {
	serverless := map[string]any{"min_capacity": 0.5, "max_capacity": 4}
	return &costv1.ResourceSet{
		Source: "terraform",
		Scopes: []*costv1.Scope{{Id: "stack", Kind: "module", Name: "root"}},
		Resources: []*costv1.Resource{
			resource("lambda", "aws_lambda_function", region, map[string]any{"memory_size": 1024, "architectures": []any{"x86_64"}}),
			resource("lambda-arm", "aws_lambda_function", region, map[string]any{"memory_size": 512, "architectures": []any{"arm64"}}),
			resource("logs", "aws_cloudwatch_log_group", region, map[string]any{}),
			resource("bucket", "aws_s3_bucket", region, map[string]any{}),
			resource("cluster", "aws_rds_cluster", region, map[string]any{"engine": "aurora-postgresql", "serverlessv2_scaling_configuration": serverless}),
			referencing(resource("writer", "aws_rds_cluster_instance", region, map[string]any{"engine": "aurora-postgresql", "instance_class": "db.serverless"}),
				map[string]string{"cluster_identifier": "cluster"}),
			resource("reader", "aws_rds_cluster_instance", region, map[string]any{"engine": "aurora-postgresql", "instance_class": "db.r6g.large"}),
			resource("mysql", "aws_rds_cluster", region, map[string]any{"engine": "aurora-mysql"}),
			resource("secret", "aws_secretsmanager_secret", region, map[string]any{}),
			resource("task", "aws_ecs_task_definition", region, map[string]any{"cpu": "512", "memory": "1024"}),
			resource("task-arm", "aws_ecs_task_definition", region, map[string]any{"cpu": "256", "memory": "512", "runtime_platform": []any{map[string]any{"cpu_architecture": "ARM64"}}}),
			referencing(resource("service", "aws_ecs_service", region, map[string]any{"desired_count": 2, "launch_type": "FARGATE"}), map[string]string{"task_definition": "task"}),
			referencing(resource("service-arm", "aws_ecs_service", region, map[string]any{"desired_count": 1,
				"capacity_provider_strategy": []any{map[string]any{"capacity_provider": "FARGATE", "weight": 1}}}), map[string]string{"task_definition": "task-arm"}),
			resource("lb", "aws_lb", region, map[string]any{"load_balancer_type": "application"}),
			resource("nat", "aws_nat_gateway", region, map[string]any{}),
			resource("eip", "aws_eip", region, map[string]any{}),
			resource("table", "aws_dynamodb_table", region, map[string]any{"billing_mode": "PAY_PER_REQUEST"}),
			resource("key", "aws_kms_key", region, map[string]any{}),
			resource("queue", "aws_sqs_queue", region, map[string]any{}),
			resource("queue-fifo", "aws_sqs_queue", region, map[string]any{"fifo_queue": true}),
			resource("topic", "aws_sns_topic", region, map[string]any{}),
			resource("topic-fifo", "aws_sns_topic", region, map[string]any{"fifo_topic": true}),
			resource("schedule", "aws_scheduler_schedule", region, map[string]any{}),
			resource("alarm", "aws_cloudwatch_metric_alarm", region, map[string]any{"metric_name": "Errors"}),
			resource("function", "aws_cloudfront_function", region, map[string]any{}),
			resource("store", "aws_cloudfront_key_value_store", region, map[string]any{}),
			resource("distribution", "aws_cloudfront_distribution", region, map[string]any{"price_class": "PriceClass_100"}),
			resource("api", "aws_api_gateway_rest_api", region, map[string]any{}),
			resource("events", "aws_appsync_api", region, map[string]any{}),
			resource("registry", "aws_ecr_repository", region, map[string]any{}),
			resource("egress", "aws_data_transfer", region, map[string]any{}),
			resource("cache", "aws_elasticache_replication_group", region, map[string]any{"engine": "valkey", "node_type": "cache.t4g.micro", "num_cache_clusters": 2, "snapshot_retention_limit": 3}),
			resource("redis", "aws_elasticache_replication_group", region, map[string]any{"engine": "redis", "node_type": "cache.r7g.large", "num_cache_clusters": 1, "snapshot_retention_limit": 2}),
			resource("vpc", "aws_vpc", region, map[string]any{}),
		},
	}
}
