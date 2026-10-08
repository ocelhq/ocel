package bastion_test

import (
	"context"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"sync"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/aws-sdk-go-v2/service/ecs"
	ecstypes "github.com/aws/aws-sdk-go-v2/service/ecs/types"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	iamtypes "github.com/aws/aws-sdk-go-v2/service/iam/types"
	"github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
)

const (
	testVPC      = "vpc-default"
	testVPCCIDR  = "172.31.0.0/16"
	testBoundary = "arn:aws:iam::123456789012:policy/ocel-app-boundary-production"
)

var testSubnets = []string{"subnet-a", "subnet-b"}

type role struct {
	arn      string
	boundary string
	trust    string
	tags     map[string]string
	policies map[string]string
}

type securityGroup struct {
	id      string
	name    string
	vpc     string
	tags    map[string]string
	ingress []ec2types.IpPermission
	egress  []ec2types.IpPermission
}

type task struct {
	arn         string
	cluster     string
	definition  string
	status      string
	agentStatus string
	stopped     bool
	stopping    int
	subnets     []string
	groups      []string
	public      bool
	exec        bool
}

type account struct {
	mu sync.Mutex

	clusters   map[string]map[string]string
	definition map[string][]*ecstypes.TaskDefinition
	roles      map[string]*role
	groups     map[string]*securityGroup
	tasks      map[string]*task

	calls         []string
	agentAfter    int
	describeCalls int
	runFails      []string
	onDescribe    func(*task)
	groupBusy     int
	nextID        int
	stopLag       int

	beforeAuthorize func(*securityGroup, []ec2types.IpPermission)
}

func newAccount() *account {
	return &account{
		clusters:   map[string]map[string]string{},
		definition: map[string][]*ecstypes.TaskDefinition{},
		roles:      map[string]*role{},
		groups:     map[string]*securityGroup{},
		tasks:      map[string]*task{},
	}
}

func (a *account) clients() bastion.Clients {
	return bastion.Clients{ECS: a, IAM: a, EC2: a}
}

func (a *account) record(call string) {
	a.calls = append(a.calls, call)
}

func (a *account) created() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, call := range a.calls {
		if strings.HasPrefix(call, "Create") || strings.HasPrefix(call, "Register") || strings.HasPrefix(call, "Put") {
			out = append(out, call)
		}
	}
	return out
}

func (a *account) called(prefix string) []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for _, call := range a.calls {
		if strings.HasPrefix(call, prefix) {
			out = append(out, call)
		}
	}
	return out
}

func (a *account) id(prefix string) string {
	a.nextID++
	return fmt.Sprintf("%s-%04d", prefix, a.nextID)
}

func (a *account) runningTasks() []string {
	a.mu.Lock()
	defer a.mu.Unlock()
	var out []string
	for arn, t := range a.tasks {
		if !t.stopped {
			out = append(out, arn)
		}
	}
	slices.Sort(out)
	return out
}

func tagMap(tags []ecstypes.Tag) map[string]string {
	out := map[string]string{}
	for _, tag := range tags {
		out[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	return out
}

func (a *account) CreateCluster(_ context.Context, in *ecs.CreateClusterInput, _ ...func(*ecs.Options)) (*ecs.CreateClusterOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	name := aws.ToString(in.ClusterName)
	if _, exists := a.clusters[name]; !exists {
		a.record("CreateCluster " + name)
		a.clusters[name] = tagMap(in.Tags)
	}
	return &ecs.CreateClusterOutput{Cluster: &ecstypes.Cluster{ClusterName: aws.String(name)}}, nil
}

func (a *account) DescribeClusters(_ context.Context, in *ecs.DescribeClustersInput, _ ...func(*ecs.Options)) (*ecs.DescribeClustersOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := &ecs.DescribeClustersOutput{}
	for _, name := range in.Clusters {
		tags, ok := a.clusters[name]
		if !ok {
			out.Failures = append(out.Failures, ecstypes.Failure{Arn: aws.String(name), Reason: aws.String("MISSING")})
			continue
		}
		cluster := ecstypes.Cluster{ClusterName: aws.String(name), Status: aws.String("ACTIVE")}
		for key, value := range tags {
			cluster.Tags = append(cluster.Tags, ecstypes.Tag{Key: aws.String(key), Value: aws.String(value)})
		}
		for _, t := range a.tasks {
			if t.cluster == name && !t.stopped {
				cluster.RunningTasksCount++
			}
		}
		out.Clusters = append(out.Clusters, cluster)
	}
	return out, nil
}

func (a *account) DeleteCluster(_ context.Context, in *ecs.DeleteClusterInput, _ ...func(*ecs.Options)) (*ecs.DeleteClusterOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	name := aws.ToString(in.Cluster)
	a.record("DeleteCluster " + name)
	for _, t := range a.tasks {
		if t.cluster == name && t.stopping > 0 {
			t.stopping--
			t.stopped = t.stopping == 0
		}
		if t.cluster == name && !t.stopped {
			return nil, &ecstypes.ClusterContainsTasksException{Message: aws.String("The Cluster cannot be deleted while Tasks are active.")}
		}
	}
	delete(a.clusters, name)
	return &ecs.DeleteClusterOutput{}, nil
}

func (a *account) RegisterTaskDefinition(_ context.Context, in *ecs.RegisterTaskDefinitionInput, _ ...func(*ecs.Options)) (*ecs.RegisterTaskDefinitionOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	family := aws.ToString(in.Family)
	a.record("RegisterTaskDefinition " + family)
	revision := int32(len(a.definition[family]) + 1)
	registered := &ecstypes.TaskDefinition{
		Family:                  in.Family,
		Revision:                revision,
		Status:                  ecstypes.TaskDefinitionStatusActive,
		TaskDefinitionArn:       aws.String(fmt.Sprintf("arn:aws:ecs:us-east-1:123456789012:task-definition/%s:%d", family, revision)),
		TaskRoleArn:             in.TaskRoleArn,
		ExecutionRoleArn:        in.ExecutionRoleArn,
		ContainerDefinitions:    in.ContainerDefinitions,
		NetworkMode:             in.NetworkMode,
		RequiresCompatibilities: in.RequiresCompatibilities,
		Cpu:                     in.Cpu,
		Memory:                  in.Memory,
	}
	a.definition[family] = append(a.definition[family], registered)
	return &ecs.RegisterTaskDefinitionOutput{TaskDefinition: registered}, nil
}

func (a *account) DescribeTaskDefinition(_ context.Context, in *ecs.DescribeTaskDefinitionInput, _ ...func(*ecs.Options)) (*ecs.DescribeTaskDefinitionOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	family, _, _ := strings.Cut(aws.ToString(in.TaskDefinition), ":")
	family = family[strings.LastIndex(family, "/")+1:]
	for i := len(a.definition[family]) - 1; i >= 0; i-- {
		if a.definition[family][i].Status == ecstypes.TaskDefinitionStatusActive {
			return &ecs.DescribeTaskDefinitionOutput{TaskDefinition: a.definition[family][i]}, nil
		}
	}
	return nil, &ecstypes.ClientException{Message: aws.String("Unable to describe task definition.")}
}

func (a *account) ListTaskDefinitions(_ context.Context, in *ecs.ListTaskDefinitionsInput, _ ...func(*ecs.Options)) (*ecs.ListTaskDefinitionsOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := &ecs.ListTaskDefinitionsOutput{}
	for _, revision := range a.definition[aws.ToString(in.FamilyPrefix)] {
		if in.Status == "" || revision.Status == in.Status {
			out.TaskDefinitionArns = append(out.TaskDefinitionArns, aws.ToString(revision.TaskDefinitionArn))
		}
	}
	return out, nil
}

func (a *account) DeregisterTaskDefinition(_ context.Context, in *ecs.DeregisterTaskDefinitionInput, _ ...func(*ecs.Options)) (*ecs.DeregisterTaskDefinitionOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	arn := aws.ToString(in.TaskDefinition)
	a.record("DeregisterTaskDefinition " + arn)
	for _, revisions := range a.definition {
		for _, revision := range revisions {
			if aws.ToString(revision.TaskDefinitionArn) == arn {
				revision.Status = ecstypes.TaskDefinitionStatusInactive
			}
		}
	}
	return &ecs.DeregisterTaskDefinitionOutput{}, nil
}

func (a *account) DeleteTaskDefinitions(_ context.Context, in *ecs.DeleteTaskDefinitionsInput, _ ...func(*ecs.Options)) (*ecs.DeleteTaskDefinitionsOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	for _, arn := range in.TaskDefinitions {
		a.record("DeleteTaskDefinitions " + arn)
		for family, revisions := range a.definition {
			a.definition[family] = slices.DeleteFunc(revisions, func(revision *ecstypes.TaskDefinition) bool {
				return aws.ToString(revision.TaskDefinitionArn) == arn
			})
		}
	}
	return &ecs.DeleteTaskDefinitionsOutput{}, nil
}

func (a *account) RunTask(_ context.Context, in *ecs.RunTaskInput, _ ...func(*ecs.Options)) (*ecs.RunTaskOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("RunTask")
	if len(a.runFails) > 0 {
		reason := a.runFails[0]
		a.runFails = a.runFails[1:]
		return &ecs.RunTaskOutput{Failures: []ecstypes.Failure{{Reason: aws.String(reason)}}}, nil
	}
	cluster := aws.ToString(in.Cluster)
	id := a.id("task")
	arn := fmt.Sprintf("arn:aws:ecs:us-east-1:123456789012:task/%s/%s", cluster, id)
	vpc := in.NetworkConfiguration.AwsvpcConfiguration
	a.tasks[arn] = &task{
		arn:        arn,
		cluster:    cluster,
		definition: aws.ToString(in.TaskDefinition),
		status:     "PROVISIONING",
		subnets:    vpc.Subnets,
		groups:     vpc.SecurityGroups,
		public:     vpc.AssignPublicIp == ecstypes.AssignPublicIpEnabled,
		exec:       in.EnableExecuteCommand,
	}
	return &ecs.RunTaskOutput{Tasks: []ecstypes.Task{a.describe(a.tasks[arn])}}, nil
}

func (a *account) describe(t *task) ecstypes.Task {
	status := t.status
	agent := t.agentStatus
	if t.stopped {
		status = "STOPPED"
	}
	return ecstypes.Task{
		TaskArn:       aws.String(t.arn),
		ClusterArn:    aws.String("arn:aws:ecs:us-east-1:123456789012:cluster/" + t.cluster),
		LastStatus:    aws.String(status),
		StoppedReason: aws.String(map[bool]string{true: "stopped in the test", false: ""}[t.stopped]),
		Containers: []ecstypes.Container{{
			Name:          aws.String("bastion"),
			RuntimeId:     aws.String("runtime-" + t.arn[strings.LastIndex(t.arn, "/")+1:]),
			LastStatus:    aws.String(status),
			ManagedAgents: []ecstypes.ManagedAgent{{Name: ecstypes.ManagedAgentNameExecuteCommandAgent, LastStatus: aws.String(agent)}},
		}},
	}
}

func (a *account) DescribeTasks(_ context.Context, in *ecs.DescribeTasksInput, _ ...func(*ecs.Options)) (*ecs.DescribeTasksOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.describeCalls++
	out := &ecs.DescribeTasksOutput{}
	for _, arn := range in.Tasks {
		t, ok := a.tasks[arn]
		if !ok {
			out.Failures = append(out.Failures, ecstypes.Failure{Arn: aws.String(arn), Reason: aws.String("MISSING")})
			continue
		}
		if a.onDescribe != nil {
			a.onDescribe(t)
		}
		if a.describeCalls > a.agentAfter && !t.stopped {
			t.status, t.agentStatus = "RUNNING", "RUNNING"
		}
		out.Tasks = append(out.Tasks, a.describe(t))
	}
	return out, nil
}

func (a *account) StopTask(_ context.Context, in *ecs.StopTaskInput, _ ...func(*ecs.Options)) (*ecs.StopTaskOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	arn := aws.ToString(in.Task)
	a.record("StopTask " + arn)
	if t, ok := a.tasks[arn]; ok && t.stopping == 0 && !t.stopped {
		t.stopping = a.stopLag
		t.stopped = a.stopLag == 0
	}
	return &ecs.StopTaskOutput{}, nil
}

func (a *account) ListTasks(_ context.Context, in *ecs.ListTasksInput, _ ...func(*ecs.Options)) (*ecs.ListTasksOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := &ecs.ListTasksOutput{}
	for arn, t := range a.tasks {
		if t.cluster == aws.ToString(in.Cluster) && !t.stopped && t.stopping == 0 {
			out.TaskArns = append(out.TaskArns, arn)
		}
	}
	slices.Sort(out.TaskArns)
	return out, nil
}

func (a *account) GetRole(_ context.Context, in *iam.GetRoleInput, _ ...func(*iam.Options)) (*iam.GetRoleOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	found, ok := a.roles[aws.ToString(in.RoleName)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{}
	}
	out := &iamtypes.Role{RoleName: in.RoleName, Arn: aws.String(found.arn)}
	if found.boundary != "" {
		out.PermissionsBoundary = &iamtypes.AttachedPermissionsBoundary{PermissionsBoundaryArn: aws.String(found.boundary)}
	}
	for key, value := range found.tags {
		out.Tags = append(out.Tags, iamtypes.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	return &iam.GetRoleOutput{Role: out}, nil
}

func (a *account) CreateRole(_ context.Context, in *iam.CreateRoleInput, _ ...func(*iam.Options)) (*iam.CreateRoleOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	name := aws.ToString(in.RoleName)
	if _, exists := a.roles[name]; exists {
		return nil, &iamtypes.EntityAlreadyExistsException{}
	}
	a.record("CreateRole " + name)
	tags := map[string]string{}
	for _, tag := range in.Tags {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	created := &role{
		arn:      "arn:aws:iam::123456789012:role/" + name,
		boundary: aws.ToString(in.PermissionsBoundary),
		trust:    aws.ToString(in.AssumeRolePolicyDocument),
		tags:     tags,
		policies: map[string]string{},
	}
	a.roles[name] = created
	return &iam.CreateRoleOutput{Role: &iamtypes.Role{RoleName: in.RoleName, Arn: aws.String(created.arn)}}, nil
}

func (a *account) GetRolePolicy(_ context.Context, in *iam.GetRolePolicyInput, _ ...func(*iam.Options)) (*iam.GetRolePolicyOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	found, ok := a.roles[aws.ToString(in.RoleName)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{}
	}
	document, ok := found.policies[aws.ToString(in.PolicyName)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{}
	}
	return &iam.GetRolePolicyOutput{PolicyName: in.PolicyName, PolicyDocument: aws.String(url.QueryEscape(document))}, nil
}

func (a *account) PutRolePolicy(_ context.Context, in *iam.PutRolePolicyInput, _ ...func(*iam.Options)) (*iam.PutRolePolicyOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	found, ok := a.roles[aws.ToString(in.RoleName)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{}
	}
	a.record("PutRolePolicy " + aws.ToString(in.RoleName))
	found.policies[aws.ToString(in.PolicyName)] = aws.ToString(in.PolicyDocument)
	return &iam.PutRolePolicyOutput{}, nil
}

func (a *account) ListRolePolicies(_ context.Context, in *iam.ListRolePoliciesInput, _ ...func(*iam.Options)) (*iam.ListRolePoliciesOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	found, ok := a.roles[aws.ToString(in.RoleName)]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{}
	}
	out := &iam.ListRolePoliciesOutput{}
	for name := range found.policies {
		out.PolicyNames = append(out.PolicyNames, name)
	}
	slices.Sort(out.PolicyNames)
	return out, nil
}

func (a *account) DeleteRolePolicy(_ context.Context, in *iam.DeleteRolePolicyInput, _ ...func(*iam.Options)) (*iam.DeleteRolePolicyOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("DeleteRolePolicy " + aws.ToString(in.RoleName))
	if found, ok := a.roles[aws.ToString(in.RoleName)]; ok {
		delete(found.policies, aws.ToString(in.PolicyName))
	}
	return &iam.DeleteRolePolicyOutput{}, nil
}

func (a *account) DeleteRole(_ context.Context, in *iam.DeleteRoleInput, _ ...func(*iam.Options)) (*iam.DeleteRoleOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	name := aws.ToString(in.RoleName)
	found, ok := a.roles[name]
	if !ok {
		return nil, &iamtypes.NoSuchEntityException{}
	}
	if len(found.policies) > 0 {
		return nil, &iamtypes.DeleteConflictException{}
	}
	a.record("DeleteRole " + name)
	delete(a.roles, name)
	return &iam.DeleteRoleOutput{}, nil
}

func (a *account) DescribeVpcs(_ context.Context, _ *ec2.DescribeVpcsInput, _ ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return &ec2.DescribeVpcsOutput{Vpcs: []ec2types.Vpc{{VpcId: aws.String(testVPC), CidrBlock: aws.String(testVPCCIDR), IsDefault: aws.Bool(true)}}}, nil
}

func (a *account) DescribeSubnets(_ context.Context, _ *ec2.DescribeSubnetsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSubnetsOutput, error) {
	out := &ec2.DescribeSubnetsOutput{}
	for _, id := range testSubnets {
		out.Subnets = append(out.Subnets, ec2types.Subnet{SubnetId: aws.String(id), VpcId: aws.String(testVPC)})
	}
	return out, nil
}

func (a *account) groupView(g *securityGroup) ec2types.SecurityGroup {
	view := ec2types.SecurityGroup{
		GroupId:             aws.String(g.id),
		GroupName:           aws.String(g.name),
		VpcId:               aws.String(g.vpc),
		IpPermissions:       slices.Clone(g.ingress),
		IpPermissionsEgress: slices.Clone(g.egress),
	}
	for key, value := range g.tags {
		view.Tags = append(view.Tags, ec2types.Tag{Key: aws.String(key), Value: aws.String(value)})
	}
	return view
}

func (a *account) DescribeSecurityGroups(_ context.Context, in *ec2.DescribeSecurityGroupsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	out := &ec2.DescribeSecurityGroupsOutput{}
	for _, g := range a.groups {
		matches := true
		for _, filter := range in.Filters {
			switch aws.ToString(filter.Name) {
			case "group-name":
				matches = matches && slices.Contains(filter.Values, g.name)
			case "vpc-id":
				matches = matches && slices.Contains(filter.Values, g.vpc)
			}
		}
		if matches {
			out.SecurityGroups = append(out.SecurityGroups, a.groupView(g))
		}
	}
	return out, nil
}

func (a *account) CreateSecurityGroup(_ context.Context, in *ec2.CreateSecurityGroupInput, _ ...func(*ec2.Options)) (*ec2.CreateSecurityGroupOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	name := aws.ToString(in.GroupName)
	for _, g := range a.groups {
		if g.name == name && g.vpc == aws.ToString(in.VpcId) {
			return nil, &smithy.GenericAPIError{Code: "InvalidGroup.Duplicate", Message: "already exists"}
		}
	}
	a.record("CreateSecurityGroup " + name)
	tags := map[string]string{}
	for _, spec := range in.TagSpecifications {
		for _, tag := range spec.Tags {
			tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
		}
	}
	created := &securityGroup{
		id:   a.id("sg"),
		name: name,
		vpc:  aws.ToString(in.VpcId),
		tags: tags,
		egress: []ec2types.IpPermission{{
			IpProtocol: aws.String("-1"),
			IpRanges:   []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}},
		}},
	}
	a.groups[created.id] = created
	return &ec2.CreateSecurityGroupOutput{GroupId: aws.String(created.id)}, nil
}

func samePermission(a, b ec2types.IpPermission) bool {
	if aws.ToString(a.IpProtocol) != aws.ToString(b.IpProtocol) || aws.ToInt32(a.FromPort) != aws.ToInt32(b.FromPort) || aws.ToInt32(a.ToPort) != aws.ToInt32(b.ToPort) {
		return false
	}
	cidrs := func(p ec2types.IpPermission) []string {
		var out []string
		for _, r := range p.IpRanges {
			out = append(out, aws.ToString(r.CidrIp))
		}
		for _, r := range p.Ipv6Ranges {
			out = append(out, aws.ToString(r.CidrIpv6))
		}
		slices.Sort(out)
		return out
	}
	return slices.Equal(cidrs(a), cidrs(b))
}

func (a *account) AuthorizeSecurityGroupEgress(_ context.Context, in *ec2.AuthorizeSecurityGroupEgressInput, _ ...func(*ec2.Options)) (*ec2.AuthorizeSecurityGroupEgressOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("AuthorizeSecurityGroupEgress " + aws.ToString(in.GroupId))
	g := a.groups[aws.ToString(in.GroupId)]
	if a.beforeAuthorize != nil {
		a.beforeAuthorize(g, in.IpPermissions)
		a.beforeAuthorize = nil
	}
	for _, wanted := range in.IpPermissions {
		if slices.ContainsFunc(g.egress, func(p ec2types.IpPermission) bool { return samePermission(p, wanted) }) {
			return nil, &smithy.GenericAPIError{Code: "InvalidPermission.Duplicate", Message: "the specified rule already exists"}
		}
	}
	g.egress = append(g.egress, in.IpPermissions...)
	return &ec2.AuthorizeSecurityGroupEgressOutput{}, nil
}

func (a *account) RevokeSecurityGroupEgress(_ context.Context, in *ec2.RevokeSecurityGroupEgressInput, _ ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupEgressOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("RevokeSecurityGroupEgress " + aws.ToString(in.GroupId))
	g := a.groups[aws.ToString(in.GroupId)]
	for _, revoked := range in.IpPermissions {
		g.egress = slices.DeleteFunc(g.egress, func(p ec2types.IpPermission) bool { return samePermission(p, revoked) })
	}
	return &ec2.RevokeSecurityGroupEgressOutput{}, nil
}

func (a *account) RevokeSecurityGroupIngress(_ context.Context, in *ec2.RevokeSecurityGroupIngressInput, _ ...func(*ec2.Options)) (*ec2.RevokeSecurityGroupIngressOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.record("RevokeSecurityGroupIngress " + aws.ToString(in.GroupId))
	g := a.groups[aws.ToString(in.GroupId)]
	for _, revoked := range in.IpPermissions {
		g.ingress = slices.DeleteFunc(g.ingress, func(p ec2types.IpPermission) bool { return samePermission(p, revoked) })
	}
	return &ec2.RevokeSecurityGroupIngressOutput{}, nil
}

func (a *account) DeleteSecurityGroup(_ context.Context, in *ec2.DeleteSecurityGroupInput, _ ...func(*ec2.Options)) (*ec2.DeleteSecurityGroupOutput, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	if a.groupBusy > 0 {
		a.groupBusy--
		return nil, &smithy.GenericAPIError{Code: "DependencyViolation", Message: "has a dependent object"}
	}
	a.record("DeleteSecurityGroup " + aws.ToString(in.GroupId))
	delete(a.groups, aws.ToString(in.GroupId))
	return &ec2.DeleteSecurityGroupOutput{}, nil
}
