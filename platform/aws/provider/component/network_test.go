package component_test

import (
	"regexp"
	"testing"

	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/component/componenttest"
)

func TestAVPCSecurityGroupDescribesItselfAndItsRuleInTheCharactersAWSAccepts(t *testing.T) {
	t.Parallel()

	at := component.ResourceCoordinate("shop", "production", "orders", naming.KindDatabase)
	rec := componenttest.Record(t, func(ctx *pulumi.Context) error {
		_, err := component.NewVPCSecurityGroup(ctx, at, component.VPCSecurityGroup{
			Subject: "the " + at.Name + " database",
			Engine:  "Postgres",
			Port:    5432,
			VPCID:   "vpc-0123",
			Clients: "sg-clients",
		})
		return err
	})

	accepted := regexp.MustCompile(`^[0-9A-Za-z_ .:/()#,@\[\]+=&;{}!$*-]*$`)
	group := rec.InputsOf(t, "aws:ec2/securityGroup:SecurityGroup", naming.ResourceID(at.Kind, at.Name, "security-group"))
	descriptions := []string{group["description"].StringValue()}
	for _, rule := range group["ingress"].ArrayValue() {
		descriptions = append(descriptions, rule.ObjectValue()["description"].StringValue())
	}
	for _, description := range descriptions {
		if !accepted.MatchString(description) {
			t.Errorf("the security group is described %q, which AWS refuses: a security group and its rules take only %s", description, accepted)
		}
	}
}
