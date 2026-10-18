package vpcclients

import (
	"context"
	"strings"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/ec2"
	ec2types "github.com/aws/aws-sdk-go-v2/service/ec2/types"
	"github.com/aws/smithy-go"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/provider"
)

type fakeEC2 struct {
	vpcs      []ec2types.Vpc
	groups    []ec2types.SecurityGroup
	created   []*ec2.CreateSecurityGroupInput
	deleteErr error
	deleted   []string
}

func (f *fakeEC2) DescribeVpcs(context.Context, *ec2.DescribeVpcsInput, ...func(*ec2.Options)) (*ec2.DescribeVpcsOutput, error) {
	return &ec2.DescribeVpcsOutput{Vpcs: f.vpcs}, nil
}

func (f *fakeEC2) DescribeSecurityGroups(_ context.Context, in *ec2.DescribeSecurityGroupsInput, _ ...func(*ec2.Options)) (*ec2.DescribeSecurityGroupsOutput, error) {
	var found []ec2types.SecurityGroup
	for _, group := range f.groups {
		if aws.ToString(group.GroupName) == in.Filters[1].Values[0] && aws.ToString(group.VpcId) == in.Filters[0].Values[0] {
			found = append(found, group)
		}
	}
	return &ec2.DescribeSecurityGroupsOutput{SecurityGroups: found}, nil
}

func (f *fakeEC2) CreateSecurityGroup(_ context.Context, in *ec2.CreateSecurityGroupInput, _ ...func(*ec2.Options)) (*ec2.CreateSecurityGroupOutput, error) {
	f.created = append(f.created, in)
	f.groups = append(f.groups, ec2types.SecurityGroup{GroupId: aws.String("sg-clients"), GroupName: in.GroupName, VpcId: in.VpcId})
	return &ec2.CreateSecurityGroupOutput{GroupId: aws.String("sg-clients")}, nil
}

func (f *fakeEC2) DeleteSecurityGroup(_ context.Context, in *ec2.DeleteSecurityGroupInput, _ ...func(*ec2.Options)) (*ec2.DeleteSecurityGroupOutput, error) {
	f.deleted = append(f.deleted, aws.ToString(in.GroupId))
	return &ec2.DeleteSecurityGroupOutput{}, f.deleteErr
}

func withDefaultVPC() *fakeEC2 {
	return &fakeEC2{vpcs: []ec2types.Vpc{{VpcId: aws.String("vpc-default"), CidrBlock: aws.String("172.31.0.0/16")}}}
}

func TestTheClientsGroupIsMadeOnceInTheDefaultVPCTaggedForItsTier(t *testing.T) {
	t.Parallel()

	api := withDefaultVPC()
	for range 2 {
		resources, err := Ensure(context.Background(), api, "ocel", environment.TierProduction, "us-east-1")
		if err != nil {
			t.Fatalf("Ensure() = %v", err)
		}
		if got := GroupOf(resources); got != "sg-clients" {
			t.Errorf("GroupOf() = %q, want the clients group", got)
		}
	}
	if len(api.created) != 1 {
		t.Fatalf("created %d groups, want one: a second ensure finds the first", len(api.created))
	}
	created := api.created[0]
	if aws.ToString(created.VpcId) != "vpc-default" || aws.ToString(created.GroupName) != "ocel-production-vpc-clients" {
		t.Errorf("created %s in %s, want ocel-production-vpc-clients in the default VPC", aws.ToString(created.GroupName), aws.ToString(created.VpcId))
	}
	tags := map[string]string{}
	for _, tag := range created.TagSpecifications[0].Tags {
		tags[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	if tags[managedByTagKey] != managedByTagValue || tags[naming.EnvTierTagKey] != string(environment.TierProduction) {
		t.Errorf("tags = %v, want the ocel and tier tags the deploy credential may delete it by", tags)
	}
}

func TestAnAccountWithNoDefaultVPCGetsNoClientsGroup(t *testing.T) {
	t.Parallel()

	api := &fakeEC2{}
	if _, err := Ensure(context.Background(), api, "ocel", environment.TierPreview, "us-east-1"); err == nil || !strings.Contains(err.Error(), "create-default-vpc") {
		t.Fatalf("Ensure() = %v, want the missing default VPC refused with how to make one", err)
	}
	if len(api.created) != 0 {
		t.Error("a group was created with no VPC to hold it")
	}
}

func TestRemovingTheClientsGroupForgetsOneAlreadyGoneAndNamesWhatStillHoldsIt(t *testing.T) {
	t.Parallel()

	recorded := []provider.PartResource{{Kind: KindSecurityGroup, ID: "sg-clients"}}
	gone := withDefaultVPC()
	gone.deleteErr = &smithy.GenericAPIError{Code: "InvalidGroup.NotFound"}
	if err := Remove(context.Background(), gone, recorded); err != nil {
		t.Errorf("Remove() = %v, want a group already gone forgotten", err)
	}
	held := withDefaultVPC()
	held.deleteErr = &smithy.GenericAPIError{Code: "DependencyViolation"}
	if err := Remove(context.Background(), held, recorded); err == nil || !strings.Contains(err.Error(), "destroy those first") {
		t.Errorf("Remove() = %v, want a group still in use refused naming what to do", err)
	}
}
