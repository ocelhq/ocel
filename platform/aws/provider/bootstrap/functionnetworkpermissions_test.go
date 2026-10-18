package bootstrap

import (
	"testing"

	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

func TestTheDeployCredentialBuildsAndTearsDownTheFunctionNetworkOnlyWhereOcelTaggedIt(t *testing.T) {
	grants := grantsOf(t, mustRender(t, DeployCredentialPermissions))
	onCreate := conditionJSON(t, taggedOnCreate())
	byOcel := conditionJSON(t, taggedByOcel())
	unscoped := conditionJSON(t, nil)

	creates := map[string][]string{
		"ec2:CreateSubnet":      {"arn:aws:ec2:*:*:subnet/*", "arn:aws:ec2:*:*:vpc/*"},
		"ec2:AllocateAddress":   {"arn:aws:ec2:*:*:elastic-ip/*"},
		"ec2:CreateNatGateway":  {"arn:aws:ec2:*:*:natgateway/*", "arn:aws:ec2:*:*:subnet/*", "arn:aws:ec2:*:*:elastic-ip/*"},
		"ec2:CreateRouteTable":  {"arn:aws:ec2:*:*:route-table/*", "arn:aws:ec2:*:*:vpc/*"},
		"ec2:CreateVpcEndpoint": {"arn:aws:ec2:*:*:vpc-endpoint/*", "arn:aws:ec2:*:*:vpc/*", "arn:aws:ec2:*:*:route-table/*"},
	}
	for action, resources := range creates {
		for _, resource := range resources {
			if !grants[grant{action: action, resource: resource, condition: onCreate}] {
				t.Errorf("the deploy credential does not grant %s on %s with Ocel's tag required on create, so the function network cannot be built or is built untagged", action, resource)
			}
		}
	}

	mutations := map[string][]string{
		"ec2:DeleteSubnet":           {"arn:aws:ec2:*:*:subnet/*"},
		"ec2:ReleaseAddress":         {"arn:aws:ec2:*:*:elastic-ip/*"},
		"ec2:DeleteNatGateway":       {"arn:aws:ec2:*:*:natgateway/*"},
		"ec2:DeleteRouteTable":       {"arn:aws:ec2:*:*:route-table/*"},
		"ec2:CreateRoute":            {"arn:aws:ec2:*:*:route-table/*"},
		"ec2:DeleteRoute":            {"arn:aws:ec2:*:*:route-table/*"},
		"ec2:AssociateRouteTable":    {"arn:aws:ec2:*:*:route-table/*", "arn:aws:ec2:*:*:subnet/*"},
		"ec2:DisassociateRouteTable": {"arn:aws:ec2:*:*:route-table/*", "arn:aws:ec2:*:*:subnet/*"},
		"ec2:DeleteVpcEndpoints":     {"arn:aws:ec2:*:*:vpc-endpoint/*"},
		"ec2:ModifyVpcEndpoint":      {"arn:aws:ec2:*:*:vpc-endpoint/*", "arn:aws:ec2:*:*:route-table/*"},
	}
	for action, resources := range mutations {
		for _, resource := range resources {
			if !grants[grant{action: action, resource: resource, condition: byOcel}] {
				t.Errorf("the deploy credential does not grant %s on %s limited to what Ocel tagged, so the function network cannot be changed or taken down, or reaches resources it does not own", action, resource)
			}
		}
	}

	reads := []string{
		"ec2:DescribeAddresses",
		"ec2:DescribeAddressesAttribute",
		"ec2:DescribeNatGateways",
		"ec2:DescribeNetworkInterfaces",
		"ec2:DescribePrefixLists",
		"ec2:DescribeRouteTables",
		"ec2:DescribeSecurityGroups",
		"ec2:DescribeSubnets",
		"ec2:DescribeVpcEndpoints",
	}
	for _, action := range reads {
		if !grants[grant{action: action, resource: iam.AnyResource, condition: unscoped}] {
			t.Errorf("the deploy credential does not grant %s, so the engine cannot read back what it built", action)
		}
	}
}
