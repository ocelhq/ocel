package cfn

import (
	"strings"

	"github.com/ocelhq/ocel/pkg/provider"
)

const globalLocation = "global"

var resourceTypes = map[string]string{
	"aws:acm/certificate:Certificate":                            "AWS::CertificateManager::Certificate",
	"aws:appautoscaling/policy:Policy":                           "AWS::ApplicationAutoScaling::ScalingPolicy",
	"aws:appautoscaling/target:Target":                           "AWS::ApplicationAutoScaling::ScalableTarget",
	"aws:appsync/api:Api":                                        "AWS::AppSync::Api",
	"aws:appsync/channelNamespace:ChannelNamespace":              "AWS::AppSync::ChannelNamespace",
	"aws:cloudfront/vpcOrigin:VpcOrigin":                         "AWS::CloudFront::VpcOrigin",
	"aws:cloudwatch/logGroup:LogGroup":                           "AWS::Logs::LogGroup",
	"aws:cloudwatch/metricAlarm:MetricAlarm":                     "AWS::CloudWatch::Alarm",
	"aws:ec2/eip:Eip":                                            "AWS::EC2::EIP",
	"aws:ec2/natGateway:NatGateway":                              "AWS::EC2::NatGateway",
	"aws:ec2/route:Route":                                        "AWS::EC2::Route",
	"aws:ec2/routeTable:RouteTable":                              "AWS::EC2::RouteTable",
	"aws:ec2/routeTableAssociation:RouteTableAssociation":        "AWS::EC2::SubnetRouteTableAssociation",
	"aws:ec2/securityGroup:SecurityGroup":                        "AWS::EC2::SecurityGroup",
	"aws:ec2/subnet:Subnet":                                      "AWS::EC2::Subnet",
	"aws:ec2/vpcEndpoint:VpcEndpoint":                            "AWS::EC2::VPCEndpoint",
	"aws:ecs/cluster:Cluster":                                    "AWS::ECS::Cluster",
	"aws:ecs/service:Service":                                    "AWS::ECS::Service",
	"aws:ecs/taskDefinition:TaskDefinition":                      "AWS::ECS::TaskDefinition",
	"aws:elasticache/parameterGroup:ParameterGroup":              "AWS::ElastiCache::ParameterGroup",
	"aws:elasticache/replicationGroup:ReplicationGroup":          "AWS::ElastiCache::ReplicationGroup",
	"aws:elasticache/subnetGroup:SubnetGroup":                    "AWS::ElastiCache::SubnetGroup",
	"aws:iam/role:Role":                                          "AWS::IAM::Role",
	"aws:iam/rolePolicy:RolePolicy":                              "AWS::IAM::RolePolicy",
	"aws:iam/rolePolicyAttachment:RolePolicyAttachment":          "aws:iam/rolePolicyAttachment:RolePolicyAttachment",
	"aws:lambda/eventSourceMapping:EventSourceMapping":           "AWS::Lambda::EventSourceMapping",
	"aws:lambda/function:Function":                               "AWS::Lambda::Function",
	"aws:lambda/functionUrl:FunctionUrl":                         "AWS::Lambda::Url",
	"aws:lambda/permission:Permission":                           "AWS::Lambda::Permission",
	"aws:lb/listener:Listener":                                   "AWS::ElasticLoadBalancingV2::Listener",
	"aws:lb/listenerRule:ListenerRule":                           "AWS::ElasticLoadBalancingV2::ListenerRule",
	"aws:lb/loadBalancer:LoadBalancer":                           "AWS::ElasticLoadBalancingV2::LoadBalancer",
	"aws:lb/targetGroup:TargetGroup":                             "AWS::ElasticLoadBalancingV2::TargetGroup",
	"aws:lb/targetGroupAttachment:TargetGroupAttachment":         "aws:lb/targetGroupAttachment:TargetGroupAttachment",
	"aws:rds/cluster:Cluster":                                    "AWS::RDS::DBCluster",
	"aws:rds/clusterInstance:ClusterInstance":                    "AWS::RDS::DBInstance",
	"aws:rds/subnetGroup:SubnetGroup":                            "AWS::RDS::DBSubnetGroup",
	"aws:s3/bucketAbac:BucketAbac":                               "aws:s3/bucketAbac:BucketAbac",
	"aws:s3/bucketCorsConfigurationV2:BucketCorsConfigurationV2": "aws:s3/bucketCorsConfigurationV2:BucketCorsConfigurationV2",
	"aws:s3/bucketNotification:BucketNotification":               "aws:s3/bucketNotification:BucketNotification",
	"aws:s3/bucketObjectv2:BucketObjectv2":                       "aws:s3/bucketObjectv2:BucketObjectv2",
	"aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock":     "aws:s3/bucketPublicAccessBlock:BucketPublicAccessBlock",
	"aws:s3/bucketV2:BucketV2":                                   "AWS::S3::Bucket",
	"aws:scheduler/schedule:Schedule":                            "AWS::Scheduler::Schedule",
	"aws:scheduler/scheduleGroup:ScheduleGroup":                  "AWS::Scheduler::ScheduleGroup",
	"aws:sns/topic:Topic":                                        "AWS::SNS::Topic",
	"aws:sns/topicSubscription:TopicSubscription":                "AWS::SNS::Subscription",
	"aws:sqs/queue:Queue":                                        "AWS::SQS::Queue",
	"aws:sqs/queuePolicy:QueuePolicy":                            "AWS::SQS::QueuePolicy",
	"ocel:index:AssetSet":                                        "ocel:index:AssetSet",
	"ocel:index:PostgresRole":                                    "ocel:index:PostgresRole",
	"ocel:index:RandomTag":                                       "ocel:index:RandomTag",
}

var globalKindPrefixes = []string{
	"AWS::IAM::", "AWS::CloudFront::", "AWS::Route53::",
	"aws:iam/", "aws:cloudfront/", "aws:route53/",
}

func ResourceTypeOf(pulumiType string) string {
	if resourceType, listed := resourceTypes[pulumiType]; listed {
		return resourceType
	}
	return pulumiType
}

func NewPhysicalResource(kind, id, region string) provider.PhysicalResource {
	location := region
	for _, prefix := range globalKindPrefixes {
		if strings.HasPrefix(kind, prefix) {
			location = globalLocation
		}
	}
	return provider.PhysicalResource{Kind: kind, ID: id, Location: location}
}
