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
			Actions:   []string{"elasticache:AddTagsToResource", "elasticache:CreateReplicationGroup"},
			Resources: []string{s.AppReplicationGroupARN(), s.AppCacheClusterARN(), s.AppCacheParameterARN(), s.AppCacheSubnetGroupARN()},
			Condition: s.TaggedOnCreateOnlyForItsTier(),
		},
		{
			Actions:   []string{"elasticache:CreateCacheParameterGroup"},
			Resources: []string{s.AppCacheParameterARN()},
			Condition: s.TaggedOnCreateOnlyForItsTier(),
		},
		{
			Actions:   []string{"elasticache:CreateCacheSubnetGroup"},
			Resources: []string{s.AppCacheSubnetGroupARN()},
			Condition: s.TaggedOnCreateOnlyForItsTier(),
		},
		{
			Actions: []string{
				"elasticache:ListTagsForResource",
				"elasticache:RemoveTagsFromResource",
			},
			Resources: []string{s.AppReplicationGroupARN(), s.AppCacheClusterARN(), s.AppCacheParameterARN(), s.AppCacheSubnetGroupARN()},
			Condition: s.TaggedByOcelForItsTier(),
		},
		{
			Actions:   []string{"elasticache:ModifyReplicationGroup"},
			Resources: []string{s.AppReplicationGroupARN(), s.AppCacheParameterARN()},
			Condition: s.TaggedByOcelForItsTier(),
		},
		{
			Actions: []string{
				"elasticache:DecreaseReplicaCount",
				"elasticache:DeleteReplicationGroup",
				"elasticache:DescribeReplicationGroups",
				"elasticache:IncreaseReplicaCount",
			},
			Resources: []string{s.AppReplicationGroupARN()},
			Condition: s.TaggedByOcelForItsTier(),
		},
		{
			Actions:   []string{"elasticache:DescribeCacheClusters"},
			Resources: []string{iam.AnyCacheClusterARN},
			Condition: s.TaggedOnlyAsItsTier(),
		},
		{
			Actions: []string{
				"elasticache:DeleteCacheParameterGroup",
				"elasticache:DescribeCacheParameterGroups",
				"elasticache:DescribeCacheParameters",
				"elasticache:ModifyCacheParameterGroup",
			},
			Resources: []string{s.AppCacheParameterARN()},
			Condition: s.TaggedByOcelForItsTier(),
		},
		{
			Actions: []string{
				"elasticache:DeleteCacheSubnetGroup",
				"elasticache:DescribeCacheSubnetGroups",
				"elasticache:ModifyCacheSubnetGroup",
			},
			Resources: []string{s.AppCacheSubnetGroupARN()},
			Condition: s.TaggedByOcelForItsTier(),
		},
		{
			Actions:   []string{"ssm:DeleteParameter", "ssm:GetParameter", "ssm:GetParametersByPath", "ssm:PutParameter"},
			Resources: []string{s.KVTokens},
		},
	}
}
