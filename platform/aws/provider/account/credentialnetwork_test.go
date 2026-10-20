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
		"StringEquals": map[string]any{"ec2:CreateAction": []string{"CreateInternetGateway", "CreateVpc"}},
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
