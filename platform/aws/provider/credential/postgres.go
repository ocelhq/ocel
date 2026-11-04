package credential

import (
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/postgres/signin"
)

func Postgres(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions: []string{
				"rds:AddTagsToResource",
				"rds:CreateDBCluster",
				"rds:CreateDBInstance",
				"rds:CreateDBSubnetGroup",
			},
			Resources: []string{s.AppClusterARN(), s.AppInstanceARN(), s.AppSubnetGroupARN()},
			Condition: s.TaggedOnCreateOnlyForItsTier(),
		},
		{
			Actions: []string{
				"rds:DeleteDBCluster",
				"rds:DeleteDBInstance",
				"rds:DeleteDBSubnetGroup",
				"rds:DescribeDBClusters",
				"rds:DescribeDBInstances",
				"rds:DescribeDBSubnetGroups",
				"rds:ListTagsForResource",
				"rds:ModifyDBCluster",
				"rds:ModifyDBInstance",
				"rds:ModifyDBSubnetGroup",
				"rds:RemoveTagsFromResource",
			},
			Resources: []string{s.AppClusterARN(), s.AppInstanceARN(), s.AppSubnetGroupARN()},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"rds:DeleteDBCluster"},
			Resources: []string{s.AppClusterSnapshotARN()},
		},
		{
			Actions:   []string{"rds:DescribeDBEngineVersions", "rds:DescribeOrderableDBInstanceOptions"},
			Resources: []string{iam.AnyResource},
		},
		{
			Actions:   []string{"rds-data:ExecuteStatement"},
			Resources: []string{s.AppClusterARN()},
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"rds-db:connect"},
			Resources: []string{"arn:aws:rds-db:*:*:dbuser:*/" + signin.AppRoleFor(s.Tier)},
		},
		{
			Actions:   []string{"secretsmanager:DescribeSecret", "secretsmanager:GetSecretValue"},
			Resources: []string{iam.AppSecretARN},
			Condition: s.ManagedByItsClusters(),
		},
	}
}
