package containers

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudfront"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ecs"
	awsiam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lb"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/logevents"
)

const (
	ecsTaskExecutionPolicyARN = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
	originListenerPort        = 80
	originTLSPort             = 443
	vpcOriginProtocolPolicy   = "http-only"
	vpcOriginTLSFloor         = "TLSv1.2"
	vpcOriginDeployTimeout    = "20m"
	vpcOriginNameSuffixBytes  = 4
	listenerDeniedStatus      = "404"
	listenerDeniedContentType = "text/plain"
	listenerDeniedBody        = "no release answers on this origin"

	cloudFrontOriginFacingPrefixList = "com.amazonaws.global.cloudfront.origin-facing"

	outputKeyVPC           = "vpcId"
	outputKeySubnets       = "subnets"
	outputKeyListener      = "listenerArn"
	outputKeyOriginHost    = "originHost"
	outputKeyVPCOrigin     = "vpcOrigin"
	outputKeyTaskSecurity  = "taskSecurityGroup"
	outputKeyCluster       = "cluster"
	outputKeyExecutionRole = "executionRoleArn"
	outputKeyLogGroup      = "logGroup"

	outputKeyPublicListener = "publicListenerArn"
	outputKeyPublicHost     = "publicHost"
	outputKeyPublicRanges   = "publicRanges"
)

var vpcOriginUnsupportedZones = []string{"use1-az3", "usw1-az2", "apne1-az3", "cac1-az3"}

type work struct {
	tier     environment.Tier
	boundary string
	rolePath string
	tags     map[string]string
	public   []string
}

func NewProgram(tier environment.Tier, boundary, rolePath string, public []string) component.Program {
	w := &work{tier: tier, boundary: boundary, rolePath: rolePath, tags: stackTags(tier), public: public}
	return component.Program{Run: w.run, Tags: w.tags}
}

func (w *work) run(ctx *pulumi.Context) error {
	vpc, err := ec2.LookupVpc(ctx, &ec2.LookupVpcArgs{Default: pulumi.BoolRef(true)})
	if err != nil {
		return fmt.Errorf("look up default VPC: %w", err)
	}
	zones, err := aws.GetAvailabilityZones(ctx, &aws.GetAvailabilityZonesArgs{ExcludeZoneIds: vpcOriginUnsupportedZones})
	if err != nil {
		return fmt.Errorf("look up the availability zones a VPC origin reaches: %w", err)
	}
	subnetIDs, err := component.LookupDefaultSubnetsIn(ctx, vpc.Id, zones.ZoneIds)
	if err != nil {
		return err
	}
	tags := pulumi.StringMap{}
	for key, value := range w.tags {
		tags[key] = pulumi.String(value)
	}
	tier := w.tier

	cluster, err := ecs.NewCluster(ctx, naming.ResourceID(naming.KindService, "cluster"), &ecs.ClusterArgs{
		Name: pulumi.String(nameFor(tier)),
		Tags: tags,
	})
	if err != nil {
		return err
	}

	edgeRanges, err := ec2.LookupManagedPrefixList(ctx, &ec2.LookupManagedPrefixListArgs{Name: pulumi.StringRef(cloudFrontOriginFacingPrefixList)})
	if err != nil {
		return fmt.Errorf("look up the addresses CloudFront reaches an origin from: %w", err)
	}
	front, err := ec2.NewSecurityGroup(ctx, naming.ResourceID(naming.KindService, "front", "security-group"), &ec2.SecurityGroupArgs{
		Name:        pulumi.String(nameFor(tier, "front")),
		Description: pulumi.String("Ocel: the load balancer every container app in the " + string(tier) + " class answers behind"),
		VpcId:       pulumi.String(vpc.Id),
		Ingress: ec2.SecurityGroupIngressArray{&ec2.SecurityGroupIngressArgs{
			Protocol:      pulumi.String("tcp"),
			FromPort:      pulumi.Int(originListenerPort),
			ToPort:        pulumi.Int(originListenerPort),
			PrefixListIds: pulumi.StringArray{pulumi.String(edgeRanges.Id)},
			Description:   pulumi.String("Ocel: only CloudFront reaches the front, through the VPC origin, and every rule behind it demands the origin secret"),
		}},
		Egress: ec2.SecurityGroupEgressArray{&ec2.SecurityGroupEgressArgs{
			Protocol: pulumi.String("-1"), FromPort: pulumi.Int(0), ToPort: pulumi.Int(0),
			CidrBlocks: pulumi.StringArray{pulumi.String("0.0.0.0/0")},
		}},
		Tags: tags,
	})
	if err != nil {
		return err
	}
	fronts := pulumi.StringArray{front.ID()}
	var public *ec2.SecurityGroup
	if len(w.public) > 0 {
		if public, err = w.createPublicSecurityGroup(ctx, vpc.Id, tags); err != nil {
			return err
		}
		fronts = append(fronts, public.ID())
	}
	tasks, err := ec2.NewSecurityGroup(ctx, naming.ResourceID(naming.KindService, "tasks", "security-group"), &ec2.SecurityGroupArgs{
		Name:        pulumi.String(nameFor(tier, "tasks")),
		Description: pulumi.String("Ocel: the tasks every container app in the " + string(tier) + " class runs as"),
		VpcId:       pulumi.String(vpc.Id),
		Ingress: ec2.SecurityGroupIngressArray{&ec2.SecurityGroupIngressArgs{
			Protocol:       pulumi.String("tcp"),
			FromPort:       pulumi.Int(TaskPort),
			ToPort:         pulumi.Int(TaskPort),
			SecurityGroups: fronts,
			Description:    pulumi.String("Ocel: only the load balancer reaches a task"),
		}},
		Egress: ec2.SecurityGroupEgressArray{&ec2.SecurityGroupEgressArgs{
			Protocol: pulumi.String("-1"), FromPort: pulumi.Int(0), ToPort: pulumi.Int(0),
			CidrBlocks: pulumi.StringArray{pulumi.String("0.0.0.0/0")},
		}},
		Tags: tags,
	})
	if err != nil {
		return err
	}

	balancer, err := lb.NewLoadBalancer(ctx, naming.ResourceID(naming.KindService, "front"), &lb.LoadBalancerArgs{
		Name:             pulumi.String(nameFor(tier)),
		LoadBalancerType: pulumi.String("application"),
		Internal:         pulumi.Bool(true),
		SecurityGroups:   pulumi.StringArray{front.ID()},
		Subnets:          pulumi.ToStringArray(subnetIDs),
		Tags:             tags,
	})
	if err != nil {
		return err
	}
	listener, err := lb.NewListener(ctx, naming.ResourceID(naming.KindService, "front", "listener"), &lb.ListenerArgs{
		LoadBalancerArn: balancer.Arn,
		Port:            pulumi.Int(originListenerPort),
		Protocol:        pulumi.String("HTTP"),
		DefaultActions: lb.ListenerDefaultActionArray{&lb.ListenerDefaultActionArgs{
			Type: pulumi.String("fixed-response"),
			FixedResponse: &lb.ListenerDefaultActionFixedResponseArgs{
				ContentType: pulumi.String(listenerDeniedContentType),
				StatusCode:  pulumi.String(listenerDeniedStatus),
				MessageBody: pulumi.String(listenerDeniedBody),
			},
		}},
		Tags: tags,
	})
	if err != nil {
		return err
	}

	suffix := make([]byte, vpcOriginNameSuffixBytes)
	if _, err := rand.Read(suffix); err != nil {
		return fmt.Errorf("name the VPC origin: %w", err)
	}
	vpcOrigin, err := cloudfront.NewVpcOrigin(ctx, naming.ResourceID(naming.KindService, "front", "vpc-origin"), &cloudfront.VpcOriginArgs{
		VpcOriginEndpointConfig: &cloudfront.VpcOriginVpcOriginEndpointConfigArgs{
			Name:                 pulumi.String(nameFor(tier, hex.EncodeToString(suffix))),
			Arn:                  balancer.Arn,
			HttpPort:             pulumi.Int(originListenerPort),
			HttpsPort:            pulumi.Int(originTLSPort),
			OriginProtocolPolicy: pulumi.String(vpcOriginProtocolPolicy),
			OriginSslProtocols: &cloudfront.VpcOriginVpcOriginEndpointConfigOriginSslProtocolsArgs{
				Items:    pulumi.StringArray{pulumi.String(vpcOriginTLSFloor)},
				Quantity: pulumi.Int(1),
			},
		},
		Timeouts: &cloudfront.VpcOriginTimeoutsArgs{
			Create: pulumi.String(vpcOriginDeployTimeout),
			Update: pulumi.String(vpcOriginDeployTimeout),
			Delete: pulumi.String(vpcOriginDeployTimeout),
		},
		Tags: tags,
	}, pulumi.DependsOn([]pulumi.Resource{listener}), pulumi.IgnoreChanges([]string{"vpcOriginEndpointConfig.name"}))
	if err != nil {
		return err
	}

	execution, err := awsiam.NewRole(ctx, naming.ResourceID(naming.KindRole, "execution"), &awsiam.RoleArgs{
		NamePrefix:          pulumi.String(nameFor(tier, "exec") + naming.WordSeparator),
		Description:         pulumi.String("Ocel: the role ECS pulls every container app's image and ships its logs with in the " + string(tier) + " tier"),
		AssumeRolePolicy:    pulumi.String(iam.AssumeRolePolicy(iam.ECSTasksPrincipal)),
		PermissionsBoundary: pulumi.String(w.boundary),
		Path:                pulumi.String(w.rolePath),
		Tags:                tags,
	})
	if err != nil {
		return err
	}
	if _, err := awsiam.NewRolePolicyAttachment(ctx, naming.ResourceID(naming.KindRole, "execution", "policy", "ecs"), &awsiam.RolePolicyAttachmentArgs{
		Role:      execution.Name,
		PolicyArn: pulumi.String(ecsTaskExecutionPolicyARN),
	}); err != nil {
		return err
	}

	logs, err := cloudwatch.NewLogGroup(ctx, naming.ResourceID(naming.KindService, "logs"), &cloudwatch.LogGroupArgs{
		Name:            pulumi.String(logevents.NameContainerGroup(tier)),
		RetentionInDays: pulumi.Int(logRetentionDays),
		Tags:            tags,
	})
	if err != nil {
		return err
	}

	encoded, err := json.Marshal(subnetIDs)
	if err != nil {
		return err
	}
	ctx.Export(outputKeyVPC, pulumi.String(vpc.Id))
	ctx.Export(outputKeySubnets, pulumi.String(encoded))
	ctx.Export(outputKeyListener, listener.Arn)
	ctx.Export(outputKeyOriginHost, balancer.DnsName)
	ctx.Export(outputKeyVPCOrigin, vpcOrigin.ID().ToStringOutput())
	ctx.Export(outputKeyTaskSecurity, tasks.ID().ToStringOutput())
	ctx.Export(outputKeyCluster, cluster.Arn)
	ctx.Export(outputKeyExecutionRole, execution.Arn)
	ctx.Export(outputKeyLogGroup, logs.Name)
	if public == nil {
		return nil
	}
	ctx.Export(outputKeyPublicRanges, pulumi.String(joinRanges(w.public)))
	return w.runPublicFront(ctx, public, subnetIDs, tags)
}

func decode(outputs auto.OutputMap) (Infrastructure, error) {
	fields := make(map[string]any, len(outputs))
	for key, value := range outputs {
		fields[key] = value.Value
	}
	var (
		infra Infrastructure
		err   error
	)
	for key, into := range map[string]*string{
		outputKeyVPC:           &infra.VPC,
		outputKeyListener:      &infra.Listener,
		outputKeyOriginHost:    &infra.OriginHost,
		outputKeyVPCOrigin:     &infra.VPCOrigin,
		outputKeyTaskSecurity:  &infra.TaskSecurity,
		outputKeyCluster:       &infra.Cluster,
		outputKeyExecutionRole: &infra.ExecutionRole,
		outputKeyLogGroup:      &infra.LogGroup,
	} {
		if *into, err = component.RequireStringField(fields, Slug, key); err != nil {
			return Infrastructure{}, err
		}
	}
	infra.PublicListener, _ = fields[outputKeyPublicListener].(string)
	infra.PublicHost, _ = fields[outputKeyPublicHost].(string)
	infra.PublicRanges, _ = fields[outputKeyPublicRanges].(string)
	encoded, err := component.RequireStringField(fields, Slug, outputKeySubnets)
	if err != nil {
		return Infrastructure{}, err
	}
	if err := json.Unmarshal([]byte(encoded), &infra.Subnets); err != nil || len(infra.Subnets) == 0 {
		return Infrastructure{}, fmt.Errorf("output %q for %s names no subnets to place a task in", outputKeySubnets, Slug)
	}
	return infra, nil
}
