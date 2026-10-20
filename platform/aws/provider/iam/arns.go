package iam

const CallerAccount = "${aws:PrincipalAccount}"

const (
	AnyKeyARN = "arn:aws:kms:*:*:key/*"

	ParameterARNPrefix = "arn:aws:ssm:*:*:parameter"

	AppSecretARN = "arn:aws:secretsmanager:*:*:secret:rds!cluster-*"

	AnyCacheClusterARN       = "arn:aws:elasticache:*:*:cluster:*"
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
	AppTargetGroupARN    = "arn:aws:elasticloadbalancing:*:*:targetgroup/*/*"
	AppTaskDefinitionARN = "arn:aws:ecs:*:*:task-definition/*:*"
	AppCertificateARN    = "arn:aws:acm:*:*:certificate/*"

	ManagedSecretClusterTagKey = "aws:rds:primaryDBClusterArn"

	ContainerVPCOriginARN   = "arn:aws:cloudfront::*:vpcorigin/*"
	VPCOriginLinkedRoleARN  = "arn:aws:iam::*:role/aws-service-role/vpcorigin.cloudfront.amazonaws.com/*"
	ECSLinkedRoleARN        = "arn:aws:iam::*:role/aws-service-role/ecs.amazonaws.com/*"
	ECSScalingLinkedRoleARN = "arn:aws:iam::*:role/aws-service-role/ecs.application-autoscaling.amazonaws.com/*"

	ScalableTargetARN = "arn:aws:application-autoscaling:*:*:scalable-target/*"
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
