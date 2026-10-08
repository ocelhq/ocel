package bastion_test

import (
	"context"
	"encoding/json"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/bastion"
)

var testSpec = bastion.Spec{Tier: environment.TierProduction, Boundary: testBoundary, Ports: []int{5432, 6379}}

func TestReconcileCreatesTheClusterTaskDefinitionRoleAndSecurityGroupOfATierTaggedAsOcels(t *testing.T) {
	t.Parallel()

	account := newAccount()
	got, err := bastion.Reconcile(context.Background(), account.clients(), testSpec)
	if err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if got.Cluster != "ocel-bastion-production" {
		t.Errorf("Reconcile().Cluster = %q, want ocel-bastion-production", got.Cluster)
	}
	if want := []string{"subnet-a", "subnet-b"}; !slices.Equal(got.Subnets, want) {
		t.Errorf("Reconcile().Subnets = %v, want the default VPC's subnets %v", got.Subnets, want)
	}
	if tags := account.clusters["ocel-bastion-production"]; tags["ocel:managed-by"] != "ocel" {
		t.Errorf("the cluster is tagged %v, want ocel:managed-by=ocel so the deploy credential may delete it", tags)
	}
	made := account.roles["ocel-bastion-production"]
	if made == nil || made.boundary != testBoundary || made.tags["ocel:managed-by"] != "ocel" {
		t.Fatalf("the task role = %+v, want one under the app boundary %s tagged ocel:managed-by=ocel", made, testBoundary)
	}
	if !strings.Contains(made.trust, "ecs-tasks.amazonaws.com") {
		t.Errorf("the task role trusts %s, want ecs-tasks.amazonaws.com", made.trust)
	}
	document := made.policies["session-channels"]
	for _, action := range []string{"ssmmessages:CreateControlChannel", "ssmmessages:CreateDataChannel", "ssmmessages:OpenControlChannel", "ssmmessages:OpenDataChannel"} {
		if !strings.Contains(document, action) {
			t.Errorf("the task role policy %s lacks %s, which the ECS Exec agent needs", document, action)
		}
	}
	definitions := account.definition["ocel-bastion-production"]
	if len(definitions) != 1 || aws.ToString(definitions[0].TaskRoleArn) != made.arn || definitions[0].ExecutionRoleArn != nil {
		t.Errorf("the task definitions = %+v, want one that runs as the task role and needs no execution role", definitions)
	}
}

func TestReconcileShapesTheSecurityGroupSoNothingReachesTheTaskAndItReachesOnlyHTTPSAndTheTargetPortsInTheVPC(t *testing.T) {
	t.Parallel()

	account := newAccount()
	if _, err := bastion.Reconcile(context.Background(), account.clients(), testSpec); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	var group *securityGroup
	for _, found := range account.groups {
		group = found
	}
	if group == nil || group.vpc != testVPC || group.tags["ocel:managed-by"] != "ocel" {
		t.Fatalf("the security group = %+v, want one in the default VPC tagged ocel:managed-by=ocel", group)
	}
	if len(group.ingress) != 0 {
		t.Errorf("the security group admits %v, want no ingress: the task is reached over Session Manager alone", group.ingress)
	}
	var got []string
	for _, rule := range group.egress {
		for _, cidr := range rule.IpRanges {
			got = append(got, aws.ToString(rule.IpProtocol)+" "+strconv.Itoa(int(aws.ToInt32(rule.FromPort)))+"-"+strconv.Itoa(int(aws.ToInt32(rule.ToPort)))+" "+aws.ToString(cidr.CidrIp))
		}
		if len(rule.Ipv6Ranges) > 0 || len(rule.PrefixListIds) > 0 || len(rule.UserIdGroupPairs) > 0 {
			t.Errorf("the security group egress rule %+v reaches beyond IPv4 CIDRs", rule)
		}
	}
	slices.Sort(got)
	want := []string{"tcp 443-443 0.0.0.0/0", "tcp 5432-5432 172.31.0.0/16", "tcp 6379-6379 172.31.0.0/16"}
	if !slices.Equal(got, want) {
		t.Errorf("the security group egress = %v, want only %v", got, want)
	}
}

func TestReconcileCreatesNothingTheSecondTimeASessionStarts(t *testing.T) {
	t.Parallel()

	account := newAccount()
	first, err := bastion.Reconcile(context.Background(), account.clients(), testSpec)
	if err != nil {
		t.Fatalf("first Reconcile() = %v", err)
	}
	made := len(account.created())

	second, err := bastion.Reconcile(context.Background(), account.clients(), testSpec)
	if err != nil {
		t.Fatalf("second Reconcile() = %v", err)
	}

	if created := account.created(); len(created) != made {
		t.Errorf("the second Reconcile() made %v, want nothing: the bastion of a tier is created once and reused", created[made:])
	}
	if first.TaskDefinition != second.TaskDefinition || first.SecurityGroup != second.SecurityGroup {
		t.Errorf("Reconcile() = %+v then %+v, want the same bastion", first, second)
	}
}

func TestReconcileClosesAnIngressAndAnEgressRuleASecurityGroupHasGrownSinceItWasCreated(t *testing.T) {
	t.Parallel()

	account := newAccount()
	if _, err := bastion.Reconcile(context.Background(), account.clients(), testSpec); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}
	for _, group := range account.groups {
		group.ingress = append(group.ingress, ec2types.IpPermission{IpProtocol: aws.String("tcp"), FromPort: aws.Int32(22), ToPort: aws.Int32(22), IpRanges: []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}}})
		group.egress = append(group.egress, ec2types.IpPermission{IpProtocol: aws.String("-1"), IpRanges: []ec2types.IpRange{{CidrIp: aws.String("0.0.0.0/0")}}})
	}

	if _, err := bastion.Reconcile(context.Background(), account.clients(), testSpec); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	for _, group := range account.groups {
		if len(group.ingress) != 0 || len(group.egress) != 3 {
			t.Errorf("the security group holds ingress %v and egress %v, want no ingress and the three egress rules", group.ingress, group.egress)
		}
	}
}

func TestReconcileLetsOnlyTheECSTasksOfTheBoundarysAccountAssumeTheTaskRole(t *testing.T) {
	t.Parallel()

	account := newAccount()
	if _, err := bastion.Reconcile(context.Background(), account.clients(), testSpec); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	var trust struct {
		Statement []struct {
			Condition map[string]map[string]string
		}
	}
	if err := json.Unmarshal([]byte(account.roles["ocel-bastion-production"].trust), &trust); err != nil || len(trust.Statement) != 1 {
		t.Fatalf("the task role trust %s = %v, want one statement", account.roles["ocel-bastion-production"].trust, err)
	}
	condition := trust.Statement[0].Condition
	if condition["StringEquals"]["aws:SourceAccount"] != "123456789012" || condition["ArnLike"]["aws:SourceArn"] != "arn:aws:ecs:*:123456789012:*" {
		t.Errorf("the task role trust is conditioned on %v, want ECS in account 123456789012 alone: without it any account's ECS is a confused deputy for the role", condition)
	}
}

func TestReconcileRefusesARoleOfTheBastionsNameThatOcelDoesNotOwn(t *testing.T) {
	t.Parallel()

	account := newAccount()
	account.roles["ocel-bastion-production"] = &role{arn: "arn:aws:iam::123456789012:role/ocel-bastion-production", tags: map[string]string{}, policies: map[string]string{}}

	_, err := bastion.Reconcile(context.Background(), account.clients(), testSpec)
	if err == nil {
		t.Fatal("Reconcile() over a role Ocel did not tag = nil error, want it refused: a task would run as a role nobody here vouches for")
	}
}

func TestReconcileRepairsARoleWhoseSessionPolicyWasNeverWritten(t *testing.T) {
	t.Parallel()

	account := newAccount()
	if _, err := bastion.Reconcile(context.Background(), account.clients(), testSpec); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}
	clear(account.roles["ocel-bastion-production"].policies)

	if _, err := bastion.Reconcile(context.Background(), account.clients(), testSpec); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	if decoded, _ := url.QueryUnescape(account.roles["ocel-bastion-production"].policies["session-channels"]); decoded == "" {
		t.Error("the task role holds no session policy after Reconcile(), so the ECS Exec agent could never open its channels")
	}
}

func TestReconcileOpensTheEgressAnotherForwardOfTheSameTierOpenedFirstWithoutFailing(t *testing.T) {
	t.Parallel()

	account := newAccount()
	account.beforeAuthorize = func(group *securityGroup, opening []ec2types.IpPermission) {
		group.egress = append(group.egress, opening[0])
	}

	if _, err := bastion.Reconcile(context.Background(), account.clients(), testSpec); err != nil {
		t.Fatalf("Reconcile() racing another forward of the tier = %v, want the rules the other opened taken as opened", err)
	}

	for _, group := range account.groups {
		if len(group.egress) != 3 {
			t.Errorf("the security group egress = %v, want the three rules once each", group.egress)
		}
	}
}

func TestReconcileRestoresASessionPolicyThatLostAnActionTheExecAgentNeeds(t *testing.T) {
	t.Parallel()

	account := newAccount()
	if _, err := bastion.Reconcile(context.Background(), account.clients(), testSpec); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}
	account.roles["ocel-bastion-production"].policies["session-channels"] = `{"Version":"2012-10-17","Statement":[{"Effect":"Allow","Action":["ssmmessages:CreateControlChannel"],"Resource":"*"}]}`

	if _, err := bastion.Reconcile(context.Background(), account.clients(), testSpec); err != nil {
		t.Fatalf("Reconcile() = %v", err)
	}

	document := account.roles["ocel-bastion-production"].policies["session-channels"]
	for _, action := range []string{"ssmmessages:CreateControlChannel", "ssmmessages:CreateDataChannel", "ssmmessages:OpenControlChannel", "ssmmessages:OpenDataChannel"} {
		if !strings.Contains(document, action) {
			t.Errorf("the session policy after Reconcile() is %s, which lacks %s, so the ECS Exec agent could never open its channels", document, action)
		}
	}
}
