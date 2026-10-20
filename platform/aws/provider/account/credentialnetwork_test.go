package account

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

func TestTheDeployCredentialBuildsAndTearsDownTheTiersNetworkOnlyWhereItIsTaggedForItsTier(t *testing.T) {
	scope := defaultNamespace.Scope(environment.TierProduction)
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	onCreate := conditionJSON(t, scope.TaggedOnCreateForItsTier())
	taggedForItsTier := conditionJSON(t, scope.TaggedForItsTier())

	creates := map[string][]string{
		"ec2:CreateVpc":             {iam.AppVPCARN},
		"ec2:CreateInternetGateway": {iam.AppInternetGatewayARN},
	}
	for action, resources := range creates {
		for _, resource := range resources {
			if !grants[grant{action: action, resource: resource, condition: onCreate}] {
				t.Errorf("the deploy credential does not grant %s on %s with its tier's tag required on create", action, resource)
			}
		}
	}
	mutations := map[string][]string{
		"ec2:DeleteVpc":             {iam.AppVPCARN},
		"ec2:ModifyVpcAttribute":    {iam.AppVPCARN},
		"ec2:AttachInternetGateway": {iam.AppInternetGatewayARN, iam.AppVPCARN},
		"ec2:DetachInternetGateway": {iam.AppInternetGatewayARN, iam.AppVPCARN},
		"ec2:DeleteInternetGateway": {iam.AppInternetGatewayARN},
	}
	for action, resources := range mutations {
		for _, resource := range resources {
			if !grants[grant{action: action, resource: resource, condition: taggedForItsTier}] {
				t.Errorf("the deploy credential does not grant %s on %s limited to what is tagged for the production tier", action, resource)
			}
		}
	}
	for g := range grants {
		if g.action == "ec2:DeleteVpc" && g.condition != taggedForItsTier {
			t.Errorf("the deploy credential grants ec2:DeleteVpc on %s under %s, want it only on the VPC tagged for its tier", g.resource, g.condition)
		}
	}
	if !grants[grant{action: "ec2:CreateTags", resource: iam.AppVPCARN, condition: conditionJSON(t, map[string]any{
		"StringEquals": map[string]any{"ec2:CreateAction": []string{"CreateInternetGateway", "CreateRouteTable", "CreateSubnet", "CreateVpc", "CreateVpcEndpoint"}},
	})}] {
		t.Error("the deploy credential cannot tag the VPC as it creates it, so the create is refused")
	}
	for _, action := range []string{"ec2:DescribeInternetGateways", "ec2:DescribeNetworkAcls", "ec2:DescribeVpcAttribute"} {
		if !grants[grant{action: action, resource: iam.AnyResource, condition: conditionJSON(t, nil)}] {
			t.Errorf("the deploy credential does not grant %s, so the engine cannot read back the VPC it built", action)
		}
	}
}

func TestEveryCredentialReadsTheVPCAndElasticIPQuotasAndNoOther(t *testing.T) {
	for purpose, document := range bothCredentials(t) {
		grants := grantsOf(t, document)
		for _, quota := range []string{"arn:aws:servicequotas:*:*:vpc/L-F678F1CE", "arn:aws:servicequotas:*:*:ec2/L-0263D0A3"} {
			if !grants[grant{action: "servicequotas:GetServiceQuota", resource: quota, condition: conditionJSON(t, nil)}] {
				t.Errorf("the %s credential cannot read quota %s, so preflight cannot say the account is out of room", purpose, quota)
			}
		}
		for g := range grants {
			if g.action == "servicequotas:GetServiceQuota" && g.resource != "arn:aws:servicequotas:*:*:vpc/L-F678F1CE" && g.resource != "arn:aws:servicequotas:*:*:ec2/L-0263D0A3" {
				t.Errorf("the %s credential reads quota %s, which no preflight checks", purpose, g.resource)
			}
		}
	}
}

func TestTheDeployCredentialCreatesSubnetsRouteTablesEndpointsAndSecurityGroupsOnlyInAVPCTaggedForItsTier(t *testing.T) {
	scope := defaultNamespace.Scope(environment.TierProduction)
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	onCreate := conditionJSON(t, scope.TaggedOnCreateForItsTier())
	taggedForItsTier := conditionJSON(t, scope.TaggedForItsTier())

	creates := map[string]string{
		"ec2:CreateSubnet":        iam.AppSubnetARN,
		"ec2:CreateRouteTable":    iam.AppRouteTableARN,
		"ec2:CreateVpcEndpoint":   iam.AppVPCEndpointARN,
		"ec2:CreateSecurityGroup": iam.AppSecurityGroupARN,
	}
	for action, created := range creates {
		if !grants[grant{action: action, resource: created, condition: onCreate}] {
			t.Errorf("the deploy credential does not grant %s on %s with its tier's tag required on create", action, created)
		}
		if !grants[grant{action: action, resource: iam.AppVPCARN, condition: taggedForItsTier}] {
			t.Errorf("the deploy credential does not grant %s in a VPC tagged for the production tier", action)
		}
		for g := range grants {
			if g.action == action && g.resource == iam.AppVPCARN && g.condition != taggedForItsTier {
				t.Errorf("the deploy credential grants %s in any VPC under %s, want only one tagged for its tier", action, g.condition)
			}
		}
	}
	if !grants[grant{action: "ec2:CreateNatGateway", resource: iam.AppSubnetARN, condition: taggedForItsTier}] {
		t.Error("the deploy credential cannot place a NAT gateway in a subnet tagged for its tier")
	}
	for g := range grants {
		if g.action == "ec2:CreateNatGateway" && g.resource == iam.AppSubnetARN && g.condition != taggedForItsTier {
			t.Errorf("the deploy credential places a NAT gateway in any subnet under %s, want only one tagged for its tier", g.condition)
		}
	}
}

func TestTheDeployCredentialChangesAndTearsDownTheNetworksSubnetsRoutesAndEndpointsOnlyWhereTheyAreTaggedForItsTier(t *testing.T) {
	scope := defaultNamespace.Scope(environment.TierProduction)
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	taggedForItsTier := conditionJSON(t, scope.TaggedForItsTier())

	mutations := map[string][]string{
		"ec2:DeleteSubnet":           {iam.AppSubnetARN},
		"ec2:DeleteRouteTable":       {iam.AppRouteTableARN},
		"ec2:CreateRoute":            {iam.AppRouteTableARN},
		"ec2:DeleteRoute":            {iam.AppRouteTableARN},
		"ec2:AssociateRouteTable":    {iam.AppRouteTableARN, iam.AppSubnetARN},
		"ec2:DisassociateRouteTable": {iam.AppRouteTableARN, iam.AppSubnetARN},
		"ec2:DeleteVpcEndpoints":     {iam.AppVPCEndpointARN},
		"ec2:ModifyVpcEndpoint":      {iam.AppVPCEndpointARN, iam.AppRouteTableARN},
	}
	for action, resources := range mutations {
		for _, resource := range resources {
			if !grants[grant{action: action, resource: resource, condition: taggedForItsTier}] {
				t.Errorf("the deploy credential does not grant %s on %s limited to what is tagged for the production tier", action, resource)
			}
		}
	}
	for _, action := range []string{"ec2:DescribeAvailabilityZones", "ec2:DescribePrefixLists", "ec2:DescribeRouteTables", "ec2:DescribeSubnets", "ec2:DescribeVpcEndpoints"} {
		if !grants[grant{action: action, resource: iam.AnyResource, condition: conditionJSON(t, nil)}] {
			t.Errorf("the deploy credential does not grant %s, so the network cannot be placed or read back", action)
		}
	}
}
