package deploy

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"

	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudfront"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/cloudwatch"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ec2"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/ecs"
	iam "github.com/pulumi/pulumi-aws/sdk/v7/go/aws/iam"
	"github.com/pulumi/pulumi-aws/sdk/v7/go/aws/lb"
	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit/provider"
	"github.com/ocelhq/ocel/pkg/providerkit/records"
	"github.com/ocelhq/ocel/pkg/providerkit/refusal"
	"github.com/ocelhq/ocel/pkg/providerkit/stackrecords"
	awsports "github.com/ocelhq/ocel/platform/aws/provider/ports"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	ContainersSlug = awsports.ContainersSlug

	consumersKey  = "consumers"
	leaseKey      = "lease"
	leaseAttempts = 5

	ecsTasksPrincipal         = "ecs-tasks.amazonaws.com"
	ecsTaskExecutionPolicyARN = "arn:aws:iam::aws:policy/service-role/AmazonECSTaskExecutionRolePolicy"
	containerLogRetentionDays = 14
	originListenerPort        = 80
	originTLSPort             = 443
	vpcOriginProtocolPolicy   = "http-only"
	vpcOriginTLSFloor         = "TLSv1.2"
	vpcOriginDeployTimeout    = "20m"
	listenerDeniedStatus      = "404"
	listenerDeniedContentType = "text/plain"
	listenerDeniedBody        = "no release answers on this origin"

	outputKeyVPC           = "vpcId"
	outputKeySubnets       = "subnets"
	outputKeyListener      = "listenerArn"
	outputKeyOriginHost    = "originHost"
	outputKeyVPCOrigin     = "vpcOrigin"
	outputKeyTaskSecurity  = "taskSecurityGroup"
	outputKeyCluster       = "cluster"
	outputKeyExecutionRole = "executionRoleArn"
	outputKeyLogGroup      = "logGroup"
)

type containerInfra struct {
	VPC           string
	Subnets       []string
	Listener      string
	OriginHost    string
	VPCOrigin     string
	TaskSecurity  string
	Cluster       string
	ExecutionRole string
	LogGroup      string
}

type containerInfraWork struct {
	class    edge.Class
	boundary string
	tags     map[string]string
	outputs  auto.OutputMap
}

const cloudFrontOriginFacingPrefixList = "com.amazonaws.global.cloudfront.origin-facing"

func containerInfraRef(class edge.Class) provider.StackRef {
	return provider.StackRef{Project: ContainersSlug, Class: class, Name: naming.InfraStack(string(class))}
}

func containerInfraName(class edge.Class, parts ...string) string {
	return naming.Join(naming.WordSeparator, append([]string{ContainersSlug, string(class)}, parts...)...)
}

func containerInfraTags(class edge.Class) map[string]string {
	return map[string]string{
		"ocel:managed-by": "ocel",
		"ocel:project":    ContainersSlug,
		"ocel:env-class":  string(class),
		"ocel:stack":      containerInfraRef(class).Name.String(),
	}
}

func consumersRecord(class edge.Class) records.Name {
	return append(stackrecords.StacksRecord(class, ContainersSlug), consumersKey)
}

func consumerRecord(ref provider.StackRef) records.Name {
	return append(consumersRecord(ref.Class), ref.Project, ref.Name.String())
}

func leaseRecord(class edge.Class) records.Name {
	return append(stackrecords.StacksRecord(class, ContainersSlug), leaseKey)
}

type lease struct {
	Destroying bool `json:"destroying,omitempty"`
}

func leaseOf(record records.Record) (lease, error) {
	var state lease
	if len(record.Bytes) == 0 {
		return state, nil
	}
	if err := json.Unmarshal(record.Bytes, &state); err != nil {
		return lease{}, fmt.Errorf("read the container infrastructure lease %s: %w", record.Name, err)
	}
	return state, nil
}

func writeLease(ctx context.Context, store records.Store, record records.Record, state lease) error {
	encoded, err := json.Marshal(state)
	if err != nil {
		return fmt.Errorf("encode the container infrastructure lease: %w", err)
	}
	record.Bytes = encoded
	_, err = store.Write(ctx, record)
	return err
}

func (r *Stacks) containerInfraFor(ctx context.Context, class edge.Class) (*release, error) {
	return r.at(ctx, containerInfraRef(class), "")
}

func (r *Stacks) readContainerInfra(ctx context.Context, class edge.Class) (containerInfra, bool, error) {
	owner, err := r.containerInfraFor(ctx, class)
	if err != nil {
		return containerInfra{}, false, err
	}
	_, present, err := stackrecords.Read(ctx, owner.cfg.Records, class, ContainersSlug, containerInfraRef(class).Name)
	if err != nil || !present {
		return containerInfra{}, false, err
	}
	outputs, err := owner.automation.Outputs(ctx, containerInfraRef(class), nil)
	if err != nil {
		return containerInfra{}, false, err
	}
	if len(outputs) == 0 {
		return containerInfra{}, false, nil
	}
	decoded, err := decodeContainerInfra(outputs)
	return decoded, err == nil, err
}

func (r *Stacks) ensureContainerInfra(ctx context.Context, ref provider.StackRef, progress edge.Progress) (containerInfra, error) {
	r.containerInfraLock.Lock()
	defer r.containerInfraLock.Unlock()
	class := ref.Class
	infra, present, err := r.readContainerInfra(ctx, class)
	if err != nil {
		return containerInfra{}, err
	}
	owner, err := r.containerInfraFor(ctx, class)
	if err != nil {
		return containerInfra{}, err
	}
	if present {
		if err := awsports.WriteContainerFront(ctx, owner.cfg.Records, class, infra.front()); err != nil {
			return containerInfra{}, err
		}
		return infra, r.claimContainerInfra(ctx, ref)
	}
	if progress != nil {
		progress.Say("Provisioning the shared container infrastructure for the " + string(class) + " class: one load balancer and one cluster every container app in it runs behind")
	}
	work := &containerInfraWork{
		class:    class,
		boundary: owner.cfg.AppBoundaryARN,
		tags:     containerInfraTags(class),
	}
	spec := provider.StackSpec{
		Ref:         containerInfraRef(class),
		Kind:        provider.StackInfra,
		Tags:        containerInfraTags(class),
		VendorState: work,
	}
	if err := stackrecords.Write(ctx, owner.cfg.Records, class, ContainersSlug, containerInfraRef(class).Name, stackrecords.Stack{
		Kind:      provider.StackInfra,
		WrittenBy: provider.WrittenByVersion(""),
	}); err != nil {
		return containerInfra{}, err
	}
	if _, err := owner.automation.Run(ctx, spec, progress); err != nil {
		return containerInfra{}, fmt.Errorf("provision the shared container infrastructure for the %s class: %w", class, err)
	}
	decoded, err := decodeContainerInfra(work.outputs)
	if err != nil {
		return containerInfra{}, err
	}
	if err := awsports.WriteContainerFront(ctx, owner.cfg.Records, class, decoded.front()); err != nil {
		return containerInfra{}, err
	}
	return decoded, r.claimContainerInfra(ctx, ref)
}

func (s containerInfra) front() awsports.ContainerFront {
	return awsports.ContainerFront{VPCOrigin: s.VPCOrigin, Host: s.OriginHost}
}

func (r *Stacks) claimContainerInfra(ctx context.Context, ref provider.StackRef) error {
	owner, err := r.containerInfraFor(ctx, ref.Class)
	if err != nil {
		return err
	}
	store := owner.cfg.Records
	for range leaseAttempts {
		leased, err := records.ReadOrEmpty(ctx, store, leaseRecord(ref.Class))
		if err != nil {
			return err
		}
		state, err := leaseOf(leased)
		if err != nil {
			return err
		}
		if state.Destroying {
			return errors.Join(
				refusal.Refuse(refusal.CodeBusy,
					"the shared container infrastructure for the %s class is being taken down by another deploy whose last container app just left; re-run this deploy once it has gone and it will provision a fresh one", ref.Class),
				records.Forget(ctx, store, consumerRecord(ref)))
		}
		record, err := records.ReadOrEmpty(ctx, store, consumerRecord(ref))
		if err != nil {
			return err
		}
		record.Bytes = []byte("{}")
		if _, err := store.Write(ctx, record); err != nil && !errors.Is(err, records.ErrStale) {
			return fmt.Errorf("record %s as a consumer of the shared container infrastructure: %w", ref.Name, err)
		}
		err = writeLease(ctx, store, leased, state)
		if err == nil {
			return nil
		}
		if !errors.Is(err, records.ErrStale) {
			return fmt.Errorf("claim the shared container infrastructure for %s: %w", ref.Name, err)
		}
	}
	return refusal.Refuse(refusal.CodeBusy,
		"the shared container infrastructure for the %s class changed hands %d times while %s was claiming it; re-run this deploy", ref.Class, leaseAttempts, ref.Name)
}

func (r *Stacks) releaseContainerInfra(ctx context.Context, store records.Store, ref provider.StackRef, progress edge.Progress) error {
	if store == nil {
		return nil
	}
	r.containerInfraLock.Lock()
	defer r.containerInfraLock.Unlock()
	consumer, err := records.ReadOrEmpty(ctx, store, consumerRecord(ref))
	if err != nil {
		return err
	}
	if len(consumer.Bytes) == 0 {
		return nil
	}
	leased, err := records.ReadOrEmpty(ctx, store, leaseRecord(ref.Class))
	if err != nil {
		return err
	}
	state, err := leaseOf(leased)
	if err != nil {
		return err
	}
	if err := records.Forget(ctx, store, consumerRecord(ref)); err != nil {
		return err
	}
	owner, err := r.containerInfraFor(ctx, ref.Class)
	if err != nil {
		return err
	}
	remaining, err := store.List(ctx, consumersRecord(ref.Class))
	if err != nil {
		return err
	}
	if len(remaining) > 0 {
		return nil
	}
	state.Destroying = true
	if err := writeLease(ctx, store, leased, state); err != nil {
		if errors.Is(err, records.ErrStale) {
			return nil
		}
		return fmt.Errorf("mark the shared container infrastructure for the %s class as going down: %w", ref.Class, err)
	}
	if progress != nil {
		progress.Say("Taking down the shared container infrastructure for the " + string(ref.Class) + " class: the last container app in it is gone")
	}
	stack := containerInfraRef(ref.Class)
	if err := owner.automation.Destroy(ctx, stack, progress); err != nil {
		return fmt.Errorf("take down the shared container infrastructure for the %s class: %w", ref.Class, err)
	}
	if err := stackrecords.Forget(ctx, store, ref.Class, ContainersSlug, stack.Name); err != nil {
		return err
	}
	if err := records.Forget(ctx, store, awsports.ContainerFrontRecord(ref.Class)); err != nil {
		return err
	}
	return records.Forget(ctx, store, leaseRecord(ref.Class))
}

func (w *containerInfraWork) run(ctx *pulumi.Context) error {
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
		Name: pulumi.String(containerInfraName(class)),
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
		Name:        pulumi.String(containerInfraName(class, "front")),
		Description: pulumi.String("Ocel: the load balancer every container app in the " + string(class) + " class answers behind"),
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
	tasks, err := ec2.NewSecurityGroup(ctx, naming.ResourceID(naming.KindService, "tasks", "security-group"), &ec2.SecurityGroupArgs{
		Name:        pulumi.String(containerInfraName(class, "tasks")),
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
		Name:             pulumi.String(containerInfraName(class)),
		LoadBalancerType: pulumi.String("application"),
		Internal:         pulumi.Bool(true),
		SecurityGroups:   pulumi.StringArray{front.ID()},
		Subnets:          pulumi.ToStringArray(subnets.Ids),
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

	vpcOrigin, err := cloudfront.NewVpcOrigin(ctx, naming.ResourceID(naming.KindService, "front", "vpc-origin"), &cloudfront.VpcOriginArgs{
		VpcOriginEndpointConfig: &cloudfront.VpcOriginVpcOriginEndpointConfigArgs{
			Name:                 pulumi.String(containerInfraName(class)),
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
	}, pulumi.DependsOn([]pulumi.Resource{listener}))
	if err != nil {
		return err
	}

	execution, err := iam.NewRole(ctx, naming.ResourceID(naming.KindRole, "execution"), &iam.RoleArgs{
		NamePrefix:          pulumi.String(containerInfraName(class, "exec") + naming.WordSeparator),
		Description:         pulumi.String("Ocel: the role ECS pulls every container app's image and ships its logs with in the " + string(class) + " class"),
		AssumeRolePolicy:    pulumi.String(assumeRolePolicy(ecsTasksPrincipal)),
		PermissionsBoundary: pulumi.String(w.boundary),
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
		RetentionInDays: pulumi.Int(containerLogRetentionDays),
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
	ctx.Export(outputKeyVPCOrigin, vpcOrigin.ID().ToStringOutput())
	ctx.Export(outputKeyTaskSecurity, tasks.ID().ToStringOutput())
	ctx.Export(outputKeyCluster, cluster.Arn)
	ctx.Export(outputKeyExecutionRole, execution.Arn)
	ctx.Export(outputKeyLogGroup, logs.Name)
	return nil
}

func decodeContainerInfra(outputs auto.OutputMap) (containerInfra, error) {
	fields := make(map[string]any, len(outputs))
	for key, value := range outputs {
		fields[key] = value.Value
	}
	var (
		infra containerInfra
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
		if *into, err = requireStringField(fields, ContainersSlug, key); err != nil {
			return containerInfra{}, err
		}
	}
	encoded, err := requireStringField(fields, ContainersSlug, outputKeySubnets)
	if err != nil {
		return containerInfra{}, err
	}
	if err := json.Unmarshal([]byte(encoded), &infra.Subnets); err != nil || len(infra.Subnets) == 0 {
		return containerInfra{}, fmt.Errorf("output %q for %s names no subnets to place a task in", outputKeySubnets, ContainersSlug)
	}
	return infra, nil
}
