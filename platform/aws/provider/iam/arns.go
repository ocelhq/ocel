package iam

import awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"

const (
	CallerAccount = "${aws:PrincipalAccount}"

	ManagedByTagKey   = "ocel:managed-by"
	ManagedByTagValue = "ocel"

	AppScopePrefix = awsports.AppScope + "-"
)

const (
	AnyKeyARN = "arn:aws:kms:*:*:key/*"

	ParameterARNPrefix = "arn:aws:ssm:*:*:parameter"

	AppBucketARN         = "arn:aws:s3:::" + AppScopePrefix + "*"
	AppFunctionARN       = "arn:aws:lambda:*:*:function:*"
	AppWorkerFunctionARN = "arn:aws:lambda:*:*:function:" + AppScopePrefix + "*"
	AppRoleARN           = "arn:aws:iam::*:role/*"
	AppSecretARN         = "arn:aws:secretsmanager:*:*:secret:rds!cluster-*"
	AppClusterARN        = "arn:aws:rds:*:*:cluster:" + AppScopePrefix + "*"
	AppInstanceARN       = "arn:aws:rds:*:*:db:" + AppScopePrefix + "*"
	AppSubnetGroupARN    = "arn:aws:rds:*:*:subgrp:" + AppScopePrefix + "*"

	AppClusterSnapshotARN = "arn:aws:rds:*:*:cluster-snapshot:" + AppScopePrefix + "*"

	AppQueueARN         = "arn:aws:sqs:*:*:" + AppScopePrefix + "*"
	AppTopicARN         = "arn:aws:sns:*:*:" + AppScopePrefix + "*"
	AppSubscriptionARN  = "arn:aws:sns:*:*:" + AppScopePrefix + "*:*"
	AppScheduleGroupARN = "arn:aws:scheduler:*:*:schedule-group/" + AppScopePrefix + "*"
	AppScheduleARN      = "arn:aws:scheduler:*:*:schedule/" + AppScopePrefix + "*/*"

	AppReplicationGroupARN   = "arn:aws:elasticache:*:*:replicationgroup:" + AppScopePrefix + "*"
	AppCacheClusterARN       = "arn:aws:elasticache:*:*:cluster:" + AppScopePrefix + "*"
	AnyCacheClusterARN       = "arn:aws:elasticache:*:*:cluster:*"
	AppCacheParameterARN     = "arn:aws:elasticache:*:*:parametergroup:" + AppScopePrefix + "*"
	AppCacheSubnetGroupARN   = "arn:aws:elasticache:*:*:subnetgroup:" + AppScopePrefix + "*"
	ElastiCacheLinkedRoleARN = "arn:aws:iam::*:role/aws-service-role/elasticache.amazonaws.com/*"

	AppEventAPIARN         = "arn:aws:appsync:*:*:apis/*"
	AppChannelNamespaceARN = "arn:aws:appsync:*:*:apis/*/channelNamespace/*"

	AppSecurityGroupARN  = "arn:aws:ec2:*:*:security-group/*"
	AppVPCARN            = "arn:aws:ec2:*:*:vpc/*"
	AppSubnetARN         = "arn:aws:ec2:*:*:subnet/*"
	AppElasticIPARN      = "arn:aws:ec2:*:*:elastic-ip/*"
	AppNATGatewayARN     = "arn:aws:ec2:*:*:natgateway/*"
	AppRouteTableARN     = "arn:aws:ec2:*:*:route-table/*"
	AppVPCEndpointARN    = "arn:aws:ec2:*:*:vpc-endpoint/*"
	AppLogGroupARN       = "arn:aws:logs:*:*:log-group:/ocel/*"
	FunctionLogGroupARN  = "arn:aws:logs:*:*:log-group:/aws/lambda/*"
	AppTargetGroupARN    = "arn:aws:elasticloadbalancing:*:*:targetgroup/*/*"
	AppTaskDefinitionARN = "arn:aws:ecs:*:*:task-definition/*:*"
	AppCertificateARN    = "arn:aws:acm:*:*:certificate/*"

	ManagedSecretClusterTagKey = "aws:rds:primaryDBClusterArn"

	ContainerClusterARN     = "arn:aws:ecs:*:*:cluster/ocel-*"
	ContainerServiceARN     = "arn:aws:ecs:*:*:service/ocel-*/*"
	ContainerBalancerARN    = "arn:aws:elasticloadbalancing:*:*:loadbalancer/app/ocel-*/*"
	ContainerListenerARN    = "arn:aws:elasticloadbalancing:*:*:listener/app/ocel-*/*/*"
	ContainerRuleARN        = "arn:aws:elasticloadbalancing:*:*:listener-rule/app/ocel-*/*/*/*"
	ContainerTrustStoreARN  = "arn:aws:elasticloadbalancing:*:*:truststore/ocel-*/*"
	ContainerVPCOriginARN   = "arn:aws:cloudfront::*:vpcorigin/*"
	VPCOriginLinkedRoleARN  = "arn:aws:iam::*:role/aws-service-role/vpcorigin.cloudfront.amazonaws.com/*"
	ECSLinkedRoleARN        = "arn:aws:iam::*:role/aws-service-role/ecs.amazonaws.com/*"
	ECSScalingLinkedRoleARN = "arn:aws:iam::*:role/aws-service-role/ecs.application-autoscaling.amazonaws.com/*"

	ScalableTargetARN = "arn:aws:application-autoscaling:*:*:scalable-target/*"
	ScalingAlarmARN   = "arn:aws:cloudwatch:*:*:alarm:TargetTracking-service/ocel-*"
	AppAlarmARN       = "arn:aws:cloudwatch:*:*:alarm:" + AppScopePrefix + "*"
	ELBLinkedRoleARN  = "arn:aws:iam::*:role/aws-service-role/elasticloadbalancing.amazonaws.com/*"

	EventSourceMappingARN = "arn:aws:lambda:*:*:event-source-mapping:*"

	EdgeDistributionARN   = "arn:aws:cloudfront::*:distribution/*"
	EdgeFunctionARNPrefix = "arn:aws:cloudfront::*:function/"
	EdgeRoutesStoreARN    = "arn:aws:cloudfront::*:key-value-store/*"
	EdgeCachePolicyARN    = "arn:aws:cloudfront::*:cache-policy/*"
	EdgeHeadersPolicyARN  = "arn:aws:cloudfront::*:response-headers-policy/*"
	EdgeAssetAccessARN    = "arn:aws:cloudfront::*:origin-access-control/*"
	RestAPIsARN           = "arn:aws:apigateway:*::/restapis"
	RestAPIResourcesARN   = "arn:aws:apigateway:*::/restapis/*"
	APIGatewayTagsARN     = "arn:aws:apigateway:*::/tags/*"
)
