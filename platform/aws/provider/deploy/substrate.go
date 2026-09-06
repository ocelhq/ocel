package deploy

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ecs"
	iam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lb"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/providerkit/ports"
)

const (
	SubstrateSlug = "ocel-containers"

	substrateConsumers = "consumers"

	ecsTasksPrincipal          = "ecs-tasks.amazonaws.com"
	ecsTaskExecutionPolicyARN  = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
	substrateLogRetentionDays  = 14
	substrateListenerPort      = 80
	substrateDeniedStatus      = "404"
	substrateDeniedContentType = "text/plain"
	substrateDeniedBody        = "no release answers on this origin"

	outputKeyVPC           = "vpcId"
	outputKeySubnets       = "subnets"
	outputKeyListener      = "listenerArn"
	outputKeyOriginHost    = "originHost"
	outputKeyTaskSecurity  = "taskSecurityGroup"
	outputKeyCluster       = "cluster"
	outputKeyExecutionRole = "executionRoleArn"
	outputKeyLogGroup      = "logGroup"
)

type substrate struct {
	VPC           string
	Subnets       []string
	Listener      string
	OriginHost    string
	TaskSecurity  string
	Cluster       string
	ExecutionRole string
	LogGroup      string
}

type substrateWork struct {
	class    providerkit.Class
	boundary string
	tags     map[string]string
	outputs  auto.OutputMap
}

const cloudFrontOriginFacingPrefixList = "com.amazonaws.global.cloudfront.origin-facing"

func substrateRef(class providerkit.Class) providerkit.StackRef {
	return providerkit.StackRef{Project: SubstrateSlug, Class: class, Name: naming.InfraStack(string(class))}
}

func substrateName(class providerkit.Class, parts ...string) string {
	return naming.Join(naming.WordSeparator, append([]string{SubstrateSlug, string(class)}, parts...)...)
}

func substrateTags(class providerkit.Class) map[string]string {
	return map[string]string{
		"ocel:managed-by": "ocel",
		"ocel:project":    SubstrateSlug,
		"ocel:env-class":  string(class),
		"ocel:stack":      substrateRef(class).Name.String(),
	}
}

func consumersRecord(class providerkit.Class) ports.RecordName {
	return append(providerkit.StacksRecord(class, SubstrateSlug), substrateConsumers)
}

func consumerRecord(ref providerkit.StackRef) ports.RecordName {
	return append(consumersRecord(ref.Class), ref.Project, ref.Name.String())
}

func (r *Releaser) substrateFor(ctx context.Context, class providerkit.Class) (*release, error) {
	return r.at(ctx, substrateRef(class), "")
}

func (r *Releaser) readSubstrate(ctx context.Context, class providerkit.Class) (substrate, bool, error) {
	held, err := r.substrateFor(ctx, class)
	if err != nil {
		return substrate{}, false, err
	}
	_, present, err := providerkit.ReadStack(ctx, held.cfg.Records, class, SubstrateSlug, substrateRef(class).Name)
	if err != nil || !present {
		return substrate{}, false, err
	}
	outputs, err := held.adapter.Outputs(ctx, substrateRef(class), nil)
	if err != nil {
		return substrate{}, false, err
	}
	if len(outputs) == 0 {
		return substrate{}, false, nil
	}
	decoded, err := decodeSubstrate(outputs)
	return decoded, err == nil, err
}

func (r *Releaser) ensureSubstrate(ctx context.Context, ref providerkit.StackRef, report providerkit.Reporter) (substrate, error) {
	r.substrates.Lock()
	defer r.substrates.Unlock()
	class := ref.Class
	held, present, err := r.readSubstrate(ctx, class)
	if err != nil {
		return substrate{}, err
	}
	if present {
		return held, r.claimSubstrate(ctx, ref)
	}
	owner, err := r.substrateFor(ctx, class)
	if err != nil {
		return substrate{}, err
	}
	if report != nil {
		report.Say("Standing up the shared container substrate for the " + string(class) + " class: one load balancer and one cluster every container app in it runs behind")
	}
	work := &substrateWork{
		class:    class,
		boundary: owner.cfg.AppBoundaryARN,
		tags:     substrateTags(class),
	}
	plan := providerkit.StackPlan{
		Ref:     substrateRef(class),
		Kind:    providerkit.StackInfra,
		Tags:    substrateTags(class),
		Options: work,
	}
	if err := providerkit.WriteStack(ctx, owner.cfg.Records, class, SubstrateSlug, substrateRef(class).Name, providerkit.Stack{
		Kind:   providerkit.StackInfra,
		Writer: providerkit.WriterFor(""),
	}); err != nil {
		return substrate{}, err
	}
	if _, err := owner.adapter.Run(ctx, plan, report); err != nil {
		return substrate{}, fmt.Errorf("stand up the container substrate for the %s class: %w", class, err)
	}
	decoded, err := decodeSubstrate(work.outputs)
	if err != nil {
		return substrate{}, err
	}
	return decoded, r.claimSubstrate(ctx, ref)
}

func (r *Releaser) claimSubstrate(ctx context.Context, ref providerkit.StackRef) error {
	owner, err := r.substrateFor(ctx, ref.Class)
	if err != nil {
		return err
	}
	record, err := ports.Held(ctx, owner.cfg.Records, consumerRecord(ref))
	if err != nil {
		return err
	}
	record.Bytes = []byte("{}")
	if _, err := owner.cfg.Records.Write(ctx, record); err != nil {
		return fmt.Errorf("record %s as a consumer of the container substrate: %w", ref.Name, err)
	}
	return nil
}

func (r *Releaser) releaseSubstrate(ctx context.Context, records providerkit.RecordStore, ref providerkit.StackRef, report providerkit.Reporter) error {
	if records == nil {
		return nil
	}
	r.substrates.Lock()
	defer r.substrates.Unlock()
	held, err := ports.Held(ctx, records, consumerRecord(ref))
	if err != nil {
		return err
	}
	if len(held.Bytes) == 0 {
		return nil
	}
	if err := ports.Forget(ctx, records, consumerRecord(ref)); err != nil {
		return err
	}
	owner, err := r.substrateFor(ctx, ref.Class)
	if err != nil {
		return err
	}
	remaining, err := records.List(ctx, consumersRecord(ref.Class))
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return nil
	}
	if report != nil {
		report.Say("Taking down the shared container substrate for the " + string(ref.Class) + " class: the last container app in it is gone")
	}
	substrate := substrateRef(ref.Class)
	if err := owner.adapter.Destroy(ctx, substrate, report); err != nil {
		return fmt.Errorf("take down the container substrate for the %s class: %w", ref.Class, err)
	}
	return providerkit.ForgetStack(ctx, records, ref.Class, SubstrateSlug, substrate.Name)
}

func (w *substrateWork) run(ctx *pulumi.Context) error {
	vpc, err := ec2.LookupVpc(ctx, &ec2.LookupVpcArgs{Default: pulumi.BoolRef(true)})
	if err != nil {
		return fmt.Errorf("look up default VPC: %w", err)
	}
	subnets, err := ec2.GetSubnets(ctx, &ec2.GetSubnetsArgs{
		Filters: []ec2.GetSubnetsFilter{{Name: "vpc-id", Values: []string{vpc.Id}}},
	})
	if err != nil {
		return fmt.Errorf("look up default VPC subnets: %w", err)
	}
	tags := pulumi.StringMap{}
	for key, value := range w.tags {
		tags[key] = pulumi.String(value)
	}
	class := w.class

	cluster, err := ecs.NewCluster(ctx, naming.ResourceID(naming.KindService, "cluster"), &ecs.ClusterArgs{
		Name: pulumi.String(substrateName(class)),
		Tags: tags,
	})
	if err != nil {
		return err
	}

	cloudfront, err := ec2.LookupManagedPrefixList(ctx, &ec2.LookupManagedPrefixListArgs{Name: pulumi.StringRef(cloudFrontOriginFacingPrefixList)})
	if err != nil {
		return fmt.Errorf("look up the addresses CloudFront reaches an origin from: %w", err)
	}
	front, err := ec2.NewSecurityGroup(ctx, naming.ResourceID(naming.KindService, "front", "security-group"), &ec2.SecurityGroupArgs{
		Name:        pulumi.String(substrateName(class, "front")),
		Description: pulumi.String("Ocel: the load balancer every container app in the " + string(class) + " class answers behind"),
		VpcId:       pulumi.String(vpc.Id),
		Ingress: ec2.SecurityGroupIngressArray{&ec2.SecurityGroupIngressArgs{
			Protocol:      pulumi.String("tcp"),
			FromPort:      pulumi.Int(substrateListenerPort),
			ToPort:        pulumi.Int(substrateListenerPort),
			PrefixListIds: pulumi.StringArray{pulumi.String(cloudfront.Id)},
			Description:   pulumi.String("Ocel: only the edge reaches the front, and every rule behind it demands the origin secret"),
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
	tasks, err := ec2.NewSecurityGroup(ctx, naming.ResourceID(naming.KindService, "tasks", "security-group"), &ec2.SecurityGroupArgs{
		Name:        pulumi.String(substrateName(class, "tasks")),
		Description: pulumi.String("Ocel: the tasks every container app in the " + string(class) + " class runs as"),
		VpcId:       pulumi.String(vpc.Id),
		Ingress: ec2.SecurityGroupIngressArray{&ec2.SecurityGroupIngressArgs{
			Protocol:       pulumi.String("tcp"),
			FromPort:       pulumi.Int(containerPortNumber),
			ToPort:         pulumi.Int(containerPortNumber),
			SecurityGroups: pulumi.StringArray{front.ID()},
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
		Name:             pulumi.String(substrateName(class)),
		LoadBalancerType: pulumi.String("application"),
		Internal:         pulumi.Bool(false),
		SecurityGroups:   pulumi.StringArray{front.ID()},
		Subnets:          pulumi.ToStringArray(subnets.Ids),
		Tags:             tags,
	})
	if err != nil {
		return err
	}
	listener, err := lb.NewListener(ctx, naming.ResourceID(naming.KindService, "front", "listener"), &lb.ListenerArgs{
		LoadBalancerArn: balancer.Arn,
		Port:            pulumi.Int(substrateListenerPort),
		Protocol:        pulumi.String("HTTP"),
		DefaultActions: lb.ListenerDefaultActionArray{&lb.ListenerDefaultActionArgs{
			Type: pulumi.String("fixed-response"),
			FixedResponse: &lb.ListenerDefaultActionFixedResponseArgs{
				ContentType: pulumi.String(substrateDeniedContentType),
				StatusCode:  pulumi.String(substrateDeniedStatus),
				MessageBody: pulumi.String(substrateDeniedBody),
			},
		}},
		Tags: tags,
	})
	if err != nil {
		return err
	}

	execution, err := iam.NewRole(ctx, naming.ResourceID(naming.KindRole, "execution"), &iam.RoleArgs{
		NamePrefix:          pulumi.String(substrateName(class, "exec") + naming.WordSeparator),
		Description:         pulumi.String("Ocel: the role ECS pulls every container app's image and ships its logs with in the " + string(class) + " class"),
		AssumeRolePolicy:    pulumi.String(assumeRolePolicy(ecsTasksPrincipal)),
		PermissionsBoundary: permissionsBoundary(w.boundary),
		Tags:                tags,
	})
	if err != nil {
		return err
	}
	if _, err := iam.NewRolePolicyAttachment(ctx, naming.ResourceID(naming.KindRole, "execution", "policy", "ecs"), &iam.RolePolicyAttachmentArgs{
		Role:      execution.Name,
		PolicyArn: pulumi.String(ecsTaskExecutionPolicyARN),
	}); err != nil {
		return err
	}

	logs, err := cloudwatch.NewLogGroup(ctx, naming.ResourceID(naming.KindService, "logs"), &cloudwatch.LogGroupArgs{
		Name:            pulumi.String("/ocel/containers/" + string(class)),
		RetentionInDays: pulumi.Int(substrateLogRetentionDays),
		Tags:            tags,
	})
	if err != nil {
		return err
	}

	encoded, err := json.Marshal(subnets.Ids)
	if err != nil {
		return err
	}
	ctx.Export(outputKeyVPC, pulumi.String(vpc.Id))
	ctx.Export(outputKeySubnets, pulumi.String(encoded))
	ctx.Export(outputKeyListener, listener.Arn)
	ctx.Export(outputKeyOriginHost, balancer.DnsName)
	ctx.Export(outputKeyTaskSecurity, tasks.ID().ToStringOutput())
	ctx.Export(outputKeyCluster, cluster.Arn)
	ctx.Export(outputKeyExecutionRole, execution.Arn)
	ctx.Export(outputKeyLogGroup, logs.Name)
	return nil
}

func decodeSubstrate(outputs auto.OutputMap) (substrate, error) {
	fields := make(map[string]any, len(outputs))
	for key, value := range outputs {
		fields[key] = value.Value
	}
	var (
		held substrate
		err  error
	)
	for key, into := range map[string]*string{
		outputKeyVPC:           &held.VPC,
		outputKeyListener:      &held.Listener,
		outputKeyOriginHost:    &held.OriginHost,
		outputKeyTaskSecurity:  &held.TaskSecurity,
		outputKeyCluster:       &held.Cluster,
		outputKeyExecutionRole: &held.ExecutionRole,
		outputKeyLogGroup:      &held.LogGroup,
	} {
		if *into, err = requireStringField(fields, SubstrateSlug, key); err != nil {
			return substrate{}, err
		}
	}
	encoded, err := requireStringField(fields, SubstrateSlug, outputKeySubnets)
	if err != nil {
		return substrate{}, err
	}
	if err := json.Unmarshal([]byte(encoded), &held.Subnets); err != nil || len(held.Subnets) == 0 {
		return substrate{}, fmt.Errorf("output %q for %s names no subnets to place a task in", outputKeySubnets, SubstrateSlug)
	}
	return held, nil
}
