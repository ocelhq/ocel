package component

const (
	TokenLambdaFunction              = "aws:lambda/function:Function"
	TokenLambdaFunctionURL           = "aws:lambda/functionUrl:FunctionUrl"
	TokenLambdaPermission            = "aws:lambda/permission:Permission"
	TokenLogGroup                    = "aws:cloudwatch/logGroup:LogGroup"
	TokenIAMRole                     = "aws:iam/role:Role"
	TokenIAMRolePolicy               = "aws:iam/rolePolicy:RolePolicy"
	TokenIAMRoleAttachment           = "aws:iam/rolePolicyAttachment:RolePolicyAttachment"
	TokenS3Bucket                    = "aws:s3/bucketV2:BucketV2"
	TokenS3PublicAccess              = "aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock"
	TokenS3CORS                      = "aws:s3/bucketCorsConfigurationV2:BucketCorsConfigurationV2"
	TokenS3Notification              = "aws:s3/bucketNotification:BucketNotification"
	TokenEC2SecurityGroup            = "aws:ec2/securityGroup:SecurityGroup"
	TokenRDSSubnetGroup              = "aws:rds/subnetGroup:SubnetGroup"
	TokenRDSCluster                  = "aws:rds/cluster:Cluster"
	TokenRDSClusterMember            = "aws:rds/clusterInstance:ClusterInstance"
	TokenElastiCacheSubnetGroup      = "aws:elasticache/subnetGroup:SubnetGroup"
	TokenElastiCacheParameterGroup   = "aws:elasticache/parameterGroup:ParameterGroup"
	TokenElastiCacheReplicationGroup = "aws:elasticache/replicationGroup:ReplicationGroup"
	TokenECSTaskDefinition           = "aws:ecs/taskDefinition:TaskDefinition"
	TokenECSService                  = "aws:ecs/service:Service"
	TokenLBTargetGroup               = "aws:lb/targetGroup:TargetGroup"
	TokenLBListenerRule              = "aws:lb/listenerRule:ListenerRule"
	TokenAppSyncAPI                  = "aws:appsync/api:Api"
	TokenAppSyncChannelNamespace     = "aws:appsync/channelNamespace:ChannelNamespace"
)

type ResourceRef struct {
	Token string
	Name  string
}

const (
	TerraformLambdaFunction              = "aws_lambda_function"
	TerraformLambdaFunctionURL           = "aws_lambda_function_url"
	TerraformLogGroup                    = "aws_cloudwatch_log_group"
	TerraformAppSyncAPI                  = "aws_appsync_api"
	TerraformS3Bucket                    = "aws_s3_bucket"
	TerraformRDSCluster                  = "aws_rds_cluster"
	TerraformRDSClusterInstance          = "aws_rds_cluster_instance"
	TerraformSecret                      = "aws_secretsmanager_secret"
	TerraformECSCluster                  = "aws_ecs_cluster"
	TerraformECSTaskDefinition           = "aws_ecs_task_definition"
	TerraformECSService                  = "aws_ecs_service"
	TerraformLoadBalancer                = "aws_lb"
	TerraformNATGateway                  = "aws_nat_gateway"
	TerraformElasticIP                   = "aws_eip"
	TerraformECRRepository               = "aws_ecr_repository"
	TerraformElastiCacheReplicationGroup = "aws_elasticache_replication_group"
	TerraformElastiCacheParameterGroup   = "aws_elasticache_parameter_group"
	TerraformElastiCacheSubnetGroup      = "aws_elasticache_subnet_group"
	TerraformSSMParameter                = "aws_ssm_parameter"
	TerraformSQSQueue                    = "aws_sqs_queue"
	TerraformSNSTopic                    = "aws_sns_topic"
	TerraformEventSourceMapping          = "aws_lambda_event_source_mapping"
	TerraformSchedulerSchedule           = "aws_scheduler_schedule"
	TerraformMetricAlarm                 = "aws_cloudwatch_metric_alarm"
	LambdaDefaultMemoryMB                = 128
	LambdaDefaultEphemeralMB             = 512
)
