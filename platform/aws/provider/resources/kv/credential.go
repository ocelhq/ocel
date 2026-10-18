package kv

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"iam:CreateServiceLinkedRole"},
			Resources: []string{iam.ElastiCacheLinkedRoleARN},
			Condition: iam.LinkedRoleFor("elasticache.amazonaws.com"),
		},
		{
			Actions: []string{
				"elasticache:AddTagsToResource",
				"elasticache:CreateReplicationGroup",
				"elasticache:ListTagsForResource",
				"elasticache:RemoveTagsFromResource",
			},
			Resources: []string{iam.AppReplicationGroupARN, iam.AppCacheClusterARN, iam.AppCacheParameterARN, iam.AppCacheSubnetGroupARN},
		},
		{
			Actions:   []string{"elasticache:ModifyReplicationGroup"},
			Resources: []string{iam.AppReplicationGroupARN, iam.AppCacheParameterARN},
		},
		{
			Actions: []string{
				"elasticache:DecreaseReplicaCount",
				"elasticache:DeleteReplicationGroup",
				"elasticache:DescribeReplicationGroups",
				"elasticache:IncreaseReplicaCount",
			},
			Resources: []string{iam.AppReplicationGroupARN},
		},
		{
			Actions:   []string{"elasticache:DescribeCacheClusters"},
			Resources: []string{iam.AnyCacheClusterARN},
		},
		{
			Actions: []string{
				"elasticache:CreateCacheParameterGroup",
				"elasticache:DeleteCacheParameterGroup",
				"elasticache:DescribeCacheParameterGroups",
				"elasticache:DescribeCacheParameters",
				"elasticache:ModifyCacheParameterGroup",
			},
			Resources: []string{iam.AppCacheParameterARN},
		},
		{
			Actions: []string{
				"elasticache:CreateCacheSubnetGroup",
				"elasticache:DeleteCacheSubnetGroup",
				"elasticache:DescribeCacheSubnetGroups",
				"elasticache:ModifyCacheSubnetGroup",
			},
			Resources: []string{iam.AppCacheSubnetGroupARN},
		},
		{
			Actions:   []string{"ssm:DeleteParameter", "ssm:GetParameter", "ssm:GetParametersByPath", "ssm:PutParameter"},
			Resources: []string{s.KVTokens},
		},
	}
}
