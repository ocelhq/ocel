package containers

import (
	"encoding/json"
	"fmt"
	"maps"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ecs"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	"github.com/ocelhq/ocel/platform/aws/provider/tier/network"
)

const (
	ecsTaskExecutionPolicyName = "AmazonECSTaskExecutionRolePolicy"
	ecsTaskExecutionPolicyARN  = "arn:aws:iam::aws:policy/service-role/" + ecsTaskExecutionPolicyName
	originListenerPort         = 80
	listenerDeniedStatus       = "404"
	listenerDeniedContentType  = "text/plain"
	listenerDeniedBody         = "no release answers on this origin"

	outputKeyVPC           = "vpcId"
	outputKeySubnets       = "subnets"
	outputKeyListener      = "listenerArn"
	outputKeyOriginHost    = "originHost"
	outputKeyVPCOrigin     = "vpcOrigin"
	outputKeyTaskSecurity  = "taskSecurityGroup"
	outputKeyCluster       = "cluster"
	outputKeyExecutionRole = "executionRoleArn"
	outputKeyLogGroup      = "logGroup"

	outputKeyPublicListener         = "publicListenerArn"
	outputKeyPublicHost             = "publicHost"
	outputKeyPublicRanges           = "publicRanges"
	outputKeyPublicRedirectListener = "publicRedirectListenerArn"
	outputKeyLivenessRole           = "livenessRoleArn"
	outputKeyLivenessTargets        = "livenessTargetGroupArn"
	outputKeyLivenessRule           = "livenessRuleArn"

	nameTagKey = "Name"
)

type work struct {
	namespace string
	tier      environment.Tier
	boundary  string
	rolePath  string
	tags      map[string]string
	fronts    Fronts
}

func NewProgram(namespace string, tier environment.Tier, boundary, rolePath string, fronts Fronts) component.Program {
	w := &work{namespace: namespace, tier: tier, boundary: boundary, rolePath: rolePath, tags: stackTags(namespace, tier), fronts: fronts}
	return component.Program{Run: w.run, Tags: w.tags}
}

func (w *work) run(ctx *pulumi.Context) error {
	vpc, err := ec2.LookupVpc(ctx, &ec2.LookupVpcArgs{Default: pulumi.BoolRef(true)})
	if err != nil {
		return fmt.Errorf("look up default VPC: %w", err)
	}
	zones, err := aws.GetAvailabilityZones(ctx, &aws.GetAvailabilityZonesArgs{ExcludeZoneIds: network.VPCOriginUnsupportedZones})
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

	cluster, err := ecs.NewCluster(ctx, nameFor(w.namespace, tier), &ecs.ClusterArgs{
		Name: pulumi.String(nameFor(w.namespace, tier)),
		Tags: tags,
	})
	if err != nil {
		return err
	}

	var sources pulumi.StringArray
	var front *ec2.SecurityGroup
	if w.fronts.Private {
		if front, err = w.createFrontSecurityGroup(ctx, vpc.Id, tags); err != nil {
			return err
		}
		sources = append(sources, front.ID())
	}
	var public *ec2.SecurityGroup
	if len(w.fronts.Public) > 0 {
		if public, err = w.createPublicSecurityGroup(ctx, vpc.Id, tags); err != nil {
			return err
		}
		sources = append(sources, public.ID())
	}
	tasks, err := ec2.NewSecurityGroup(ctx, tasksGroupName(w.namespace, tier), &ec2.SecurityGroupArgs{
		Name:        pulumi.String(tasksGroupName(w.namespace, tier)),
		Description: pulumi.String("Ocel: the tasks every container app in the " + string(tier) + " class runs as"),
		VpcId:       pulumi.String(vpc.Id),
		Ingress: ec2.SecurityGroupIngressArray{&ec2.SecurityGroupIngressArgs{
			Protocol:       pulumi.String("tcp"),
			FromPort:       pulumi.Int(TaskPort),
			ToPort:         pulumi.Int(TaskPort),
			SecurityGroups: sources,
			Description:    pulumi.String("Ocel: only the load balancer reaches a task"),
		}},
		Egress: ec2.SecurityGroupEgressArray{&ec2.SecurityGroupEgressArgs{
			Protocol: pulumi.String("-1"), FromPort: pulumi.Int(0), ToPort: pulumi.Int(0),
			CidrBlocks: pulumi.StringArray{pulumi.String("0.0.0.0/0")},
		}},
		Tags: namedTags(tags, tasksGroupName(w.namespace, tier)),
	})
	if err != nil {
		return err
	}
	if front != nil {
		if err := w.runPrivateFront(ctx, front, subnetIDs, tags); err != nil {
			return err
		}
	}

	execution, _, err := component.NewAppRole(ctx, component.AppRole{
		Name:        component.NameOf(executionRoleName(w.namespace, tier)),
		Description: pulumi.String("Ocel: the role ECS pulls every container app's image and ships its logs with in the " + string(tier) + " tier"),
		Principal:   iam.ECSTasksPrincipal,
		Boundary:    w.boundary,
		Path:        w.rolePath,
		Tags:        w.tags,
		Managed:     []string{ecsTaskExecutionPolicyARN},
	})
	if err != nil {
		return err
	}

	logs, err := cloudwatch.NewLogGroup(ctx, awsports.ContainerLogGroupName(w.namespace, tier), &cloudwatch.LogGroupArgs{
		Name:            pulumi.String(awsports.ContainerLogGroupName(w.namespace, tier)),
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
	ctx.Export(outputKeyTaskSecurity, tasks.ID().ToStringOutput())
	ctx.Export(outputKeyCluster, cluster.Arn)
	ctx.Export(outputKeyExecutionRole, execution.Arn)
	ctx.Export(outputKeyLogGroup, logs.Name)
	if public == nil {
		return nil
	}
	ctx.Export(outputKeyPublicRanges, pulumi.String(joinRanges(w.fronts.Public)))
	return w.runPublicFront(ctx, public, subnetIDs, tags)
}

func decode(outputs auto.OutputMap, private bool) (Infrastructure, error) {
	fields := make(map[string]any, len(outputs))
	for key, value := range outputs {
		fields[key] = value.Value
	}
	var (
		infra Infrastructure
		err   error
	)
	required := map[string]*string{
		outputKeyVPC:           &infra.VPC,
		outputKeyTaskSecurity:  &infra.TaskSecurity,
		outputKeyCluster:       &infra.Cluster,
		outputKeyExecutionRole: &infra.ExecutionRole,
		outputKeyLogGroup:      &infra.LogGroup,
	}
	if _, raised := fields[outputKeyListener]; private || raised {
		required[outputKeyListener] = &infra.Listener
		required[outputKeyOriginHost] = &infra.OriginHost
		required[outputKeyVPCOrigin] = &infra.VPCOrigin
	}
	for key, into := range required {
		if *into, err = component.RequireStringField(fields, Slug, key); err != nil {
			return Infrastructure{}, err
		}
	}
	for key, into := range map[string]*string{
		outputKeyPublicListener:         &infra.PublicListener,
		outputKeyPublicHost:             &infra.PublicHost,
		outputKeyPublicRanges:           &infra.PublicRanges,
		outputKeyPublicRedirectListener: &infra.PublicRedirectListener,
		outputKeyLivenessRole:           &infra.LivenessRole,
		outputKeyLivenessTargets:        &infra.LivenessTargets,
		outputKeyLivenessRule:           &infra.LivenessRule,
	} {
		*into, _ = fields[key].(string)
	}
	encoded, err := component.RequireStringField(fields, Slug, outputKeySubnets)
	if err != nil {
		return Infrastructure{}, err
	}
	if err := json.Unmarshal([]byte(encoded), &infra.Subnets); err != nil || len(infra.Subnets) == 0 {
		return Infrastructure{}, fmt.Errorf("output %q for %s names no subnets to place a task in", outputKeySubnets, Slug)
	}
	return infra, nil
}

func namedTags(tags pulumi.StringMap, name string) pulumi.StringMap {
	named := pulumi.StringMap{nameTagKey: pulumi.String(name)}
	maps.Copy(named, tags)
	return named
}
