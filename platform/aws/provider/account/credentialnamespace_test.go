package account

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
)

type installResource struct {
	action string
	arn    string
}

func resourcesOfInstall(ns Namespace, tier environment.Tier) []installResource {
	const region = "us-east-1:111122223333"
	fence := naming.AppFence(string(ns))
	return []installResource{
		{"lambda:UpdateFunctionCode", "arn:aws:lambda:" + region + ":function:" + fence + "shop-prod-web-api-r1a2b3c4d"},
		{"lambda:DeleteFunction", "arn:aws:lambda:" + region + ":function:" + fence + "shop-prod-web-api-r1a2b3c4d"},
		{"iam:DeleteRole", "arn:aws:iam::111122223333:role" + ns.AppRolePathFor(tier) + fence + "shop-prod-web-r1a2b3c4d"},
		{"sqs:DeleteQueue", "arn:aws:sqs:" + region + ":" + fence + "shop-prod-orders-1a2b3c"},
		{"sns:DeleteTopic", "arn:aws:sns:" + region + ":" + fence + "shop-prod-orders-1a2b3c"},
		{"scheduler:DeleteScheduleGroup", "arn:aws:scheduler:" + region + ":schedule-group/" + fence + "shop-prod-heartbeat-1a2b3c"},
		{"rds:DeleteDBCluster", "arn:aws:rds:" + region + ":cluster:" + fence + naming.TierWord(tier) + "-shop-prod-main-k3x9q0"},
		{"s3:DeleteBucket", "arn:aws:s3:::" + fence + "shop-prod-uploads-k3x9q0"},
		{"elasticache:DeleteReplicationGroup", "arn:aws:elasticache:" + region + ":replicationgroup:" + fence + "shop-prod-cache-1a2b3c"},
		{"cloudwatch:DeleteAlarms", "arn:aws:cloudwatch:" + region + ":alarm:" + fence + "shop-prod-jobs-backlog-1a2b3c"},
		{"ec2:DeleteSecurityGroup", "arn:aws:ec2:" + region + ":security-group/sg-0123456789abcdef0"},
		{"logs:DeleteLogGroup", "arn:aws:logs:" + region + ":log-group:/aws/lambda/" + fence + "shop-prod-web-api-r1a2b3c4d"},
	}
}

func taggedByInstall(ns Namespace, tier environment.Tier) requestContext {
	return requestContext{
		"aws:ResourceTag/" + naming.ManagedByTagKey: string(ns),
		"aws:ResourceTag/" + naming.EnvTierTagKey:   string(tier),
		"aws:ResourceAccount":                       "111122223333",
		"aws:PrincipalAccount":                      "111122223333",
	}
}

func TestACredentialReachesTheResourcesOfOnlyItsOwnNamespace(t *testing.T) {
	renders := map[string]func(Namespace, environment.Tier, string) (string, error){
		"deploy":    DeployCredentialPermissions,
		"bootstrap": BootstrapCredentialPermissions,
	}
	for _, pair := range [][2]Namespace{{"ocel", "other"}, {"other", "ocel"}} {
		own, other := pair[0], pair[1]
		for _, tier := range bothTiers {
			for purpose, render := range renders {
				document, err := render(own, tier, "")
				if err != nil {
					t.Fatalf("render the %s %s credential of namespace %s: %v", tier, purpose, own, err)
				}
				for _, r := range resourcesOfInstall(own, tier) {
					if !allows(t, document, r.action, r.arn, taggedByInstall(own, tier)) {
						t.Errorf("the %s %s credential of namespace %s cannot %s on its own %s", tier, purpose, own, r.action, r.arn)
					}
				}
				for _, r := range resourcesOfInstall(other, tier) {
					if allows(t, document, r.action, r.arn, taggedByInstall(other, tier)) {
						t.Errorf("the %s %s credential of namespace %s can %s on %s, a resource of namespace %s", tier, purpose, own, r.action, r.arn, other)
					}
				}
			}
		}
	}
}
