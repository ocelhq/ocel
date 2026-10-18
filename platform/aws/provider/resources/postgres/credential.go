package postgres

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func credential(iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions: []string{
				"rds:AddTagsToResource",
				"rds:CreateDBCluster",
				"rds:CreateDBInstance",
				"rds:CreateDBSubnetGroup",
			},
			Resources: []string{iam.AppClusterARN, iam.AppInstanceARN, iam.AppSubnetGroupARN},
			Condition: iam.TaggedOnCreate(),
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
			Resources: []string{iam.AppClusterARN, iam.AppInstanceARN, iam.AppSubnetGroupARN},
			Condition: iam.TaggedByOcel(),
		},
		{
			Actions:   []string{"rds:DeleteDBCluster"},
			Resources: []string{iam.AppClusterSnapshotARN},
		},
		{
			Actions:   []string{"rds:DescribeDBEngineVersions", "rds:DescribeOrderableDBInstanceOptions"},
			Resources: []string{iam.AnyResource},
		},
		{
			Actions:   []string{"rds-data:ExecuteStatement"},
			Resources: []string{iam.AppClusterARN},
		},
		{
			Actions:   []string{"rds-db:connect"},
			Resources: []string{"arn:aws:rds-db:*:*:dbuser:*/" + AppRole},
		},
		{
			Actions:   []string{"secretsmanager:DescribeSecret", "secretsmanager:GetSecretValue"},
			Resources: []string{iam.AppSecretARN},
			Condition: iam.ManagedByAnAppCluster(),
		},
	}
}
