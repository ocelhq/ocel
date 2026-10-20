package account

import (
	"maps"
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
	"github.com/ocelhq/ocel/platform/aws/provider/resources/postgres"
)

func TestEveryCredentialReachesOnlyTheBucketsAndClustersDeploysNameUnderTheAppScope(t *testing.T) {
	var bootstrapStores []string
	for _, tier := range bothTiers {
		bootstrapARNs := defaultNamespace.Scope(tier)
		bootstrapStores = slices.Concat(bootstrapStores, bootstrapARNs.BootstrapBuckets, bootstrapARNs.BootstrapObjects)
	}
	for purpose, document := range bothCredentials(t) {
		for g := range grantsOf(t, document) {
			switch {
			case strings.HasPrefix(g.action, "s3:"):
				if slices.Contains(bootstrapStores, g.resource) {
					continue
				}
				if !strings.HasPrefix(g.resource, "arn:aws:s3:::"+naming.AppFence(string(defaultNamespace))) {
					t.Errorf("the %s credential grants %s on %s, a bucket name a deploy never creates: S3 evaluates no Ocel tag on a bucket, so the name prefix is the only scope", purpose, g.action, g.resource)
				}
			case strings.HasPrefix(g.action, "rds:") && !readOnly(g.action):
				for _, kind := range []string{"cluster:", "cluster-snapshot:", "db:", "subgrp:"} {
					if strings.Contains(g.resource, ":"+kind) && !strings.Contains(g.resource, ":"+kind+naming.AppFence(string(defaultNamespace))) {
						t.Errorf("the %s credential grants %s on %s, an identifier a deploy never mints", purpose, g.action, g.resource)
					}
				}
			case strings.HasPrefix(g.action, "secretsmanager:") && g.resource == iam.AppSecretARN:
				if !slices.Contains([]string{conditionJSON(t, defaultNamespace.Scope(environment.TierProduction).ManagedByItsClusters()), conditionJSON(t, defaultNamespace.Scope(environment.TierPreview).ManagedByItsClusters())}, g.condition) {
					t.Errorf("the %s credential grants %s on %s under %s, want the secret pinned to a cluster in the app scope through the tag RDS stamps on it, or the credential reads every Aurora master password in the account", purpose, g.action, g.resource, g.condition)
				}
			}
		}
	}
}

func TestEveryCredentialMayDeleteAClusterIntoAFinalSnapshotUnderTheAppScope(t *testing.T) {
	for purpose, document := range bothCredentials(t) {
		want := grant{
			action:    "rds:DeleteDBCluster",
			resource:  defaultNamespace.Scope(tierOf(purpose)).AppClusterSnapshotARN(),
			condition: conditionJSON(t, nil),
		}
		if !grantsOf(t, document)[want] {
			t.Errorf("the %s credential cannot delete a cluster into a final snapshot: RDS authorizes DeleteDBCluster on the snapshot it creates as well, and that snapshot carries no tag yet", purpose)
		}
	}
}

func TestEveryCredentialProvisionsKVStoresOnlyUnderTheAppScope(t *testing.T) {
	group, member := "arn:aws:elasticache:*:*:replicationgroup:ocel-app-*", "arn:aws:elasticache:*:*:cluster:ocel-app-*"
	parameters, subnets := "arn:aws:elasticache:*:*:parametergroup:ocel-app-*", "arn:aws:elasticache:*:*:subnetgroup:ocel-app-*"
	on := map[string][]string{
		"elasticache:CreateReplicationGroup":       {group, member, parameters, subnets},
		"elasticache:ModifyReplicationGroup":       {group, parameters},
		"elasticache:DeleteReplicationGroup":       {group},
		"elasticache:DescribeReplicationGroups":    {group},
		"elasticache:IncreaseReplicaCount":         {group},
		"elasticache:DecreaseReplicaCount":         {group},
		"elasticache:DescribeCacheClusters":        {"arn:aws:elasticache:*:*:cluster:*"},
		"elasticache:CreateCacheParameterGroup":    {parameters},
		"elasticache:ModifyCacheParameterGroup":    {parameters},
		"elasticache:DeleteCacheParameterGroup":    {parameters},
		"elasticache:DescribeCacheParameterGroups": {parameters},
		"elasticache:DescribeCacheParameters":      {parameters},
		"elasticache:CreateCacheSubnetGroup":       {subnets},
		"elasticache:ModifyCacheSubnetGroup":       {subnets},
		"elasticache:DeleteCacheSubnetGroup":       {subnets},
		"elasticache:DescribeCacheSubnetGroups":    {subnets},
		"elasticache:AddTagsToResource":            {group, member, parameters, subnets},
		"elasticache:ListTagsForResource":          {group, member, parameters, subnets},
		"elasticache:RemoveTagsFromResource":       {group, member, parameters, subnets},
	}
	want := map[grant]bool{}
	for action, resources := range on {
		for _, resource := range resources {
			want[grant{action: action, resource: resource, condition: conditionJSON(t, nil)}] = true
		}
	}
	for purpose, document := range bothCredentials(t) {
		granted := map[grant]bool{}
		for g := range grantsOf(t, document) {
			if strings.HasPrefix(g.action, "elasticache:") {
				g.condition = conditionJSON(t, nil)
				granted[g] = true
			}
		}
		if !maps.Equal(granted, want) {
			t.Errorf("the %s credential grants elasticache %v, want exactly %v: a store's replication group, members, parameter group and subnet group, each named under the app scope", purpose, granted, want)
		}
	}
}

func TestEveryCredentialMayCreateElastiCachesLinkedRole(t *testing.T) {
	want := grant{
		action:    "iam:CreateServiceLinkedRole",
		resource:  "arn:aws:iam::*:role/aws-service-role/elasticache.amazonaws.com/*",
		condition: conditionJSON(t, iam.LinkedRoleFor("elasticache.amazonaws.com")),
	}
	for purpose, document := range bothCredentials(t) {
		if !grantsOf(t, document)[want] {
			t.Errorf("the %s credential cannot create ElastiCache's service-linked role, which the first store in an account needs to place its nodes in the VPC", purpose)
		}
	}
}

func TestEveryCredentialReadsWritesAndDeletesTheKVTokensAndNoOtherParameterThatWay(t *testing.T) {
	for purpose, document := range bothCredentials(t) {
		tokens := iam.ParameterARNPrefix + defaultNamespace.KVTokenRoot() + "/" + string(tierOf(purpose)) + "/*"
		granted := grantsOf(t, document)
		for _, action := range []string{"ssm:GetParameter", "ssm:GetParametersByPath", "ssm:PutParameter", "ssm:DeleteParameter"} {
			if !granted[grant{action: action, resource: tokens, condition: conditionJSON(t, nil)}] {
				t.Errorf("the %s credential does not grant %s on %s, where a deploy keeps each store's AUTH token", purpose, action, tokens)
			}
		}
	}
}

func TestEveryCredentialRunsEventAPIsOnlyOnWhatItTagged(t *testing.T) {
	apis, namespaces := "arn:aws:appsync:*:*:apis/*", "arn:aws:appsync:*:*:apis/*/channelNamespace/*"
	for purpose, document := range bothCredentials(t) {
		s := defaultNamespace.Scope(tierOf(purpose))
		want := map[grant]bool{
			{action: "appsync:CreateApi", resource: iam.AnyResource, condition: conditionJSON(t, s.TaggedOnCreateForItsTier())}:             true,
			{action: "appsync:CreateChannelNamespace", resource: namespaces, condition: conditionJSON(t, s.TaggedOnCreateOnlyForItsTier())}: true,
		}
		for _, resource := range []string{apis, namespaces} {
			want[grant{action: "appsync:TagResource", resource: resource, condition: conditionJSON(t, s.RetaggedByOcelOnlyAsItsTier())}] = true
		}
		for _, action := range []string{
			"appsync:DeleteApi",
			"appsync:DeleteChannelNamespace",
			"appsync:GetApi",
			"appsync:GetChannelNamespace",
			"appsync:ListTagsForResource",
			"appsync:UntagResource",
			"appsync:UpdateApi",
			"appsync:UpdateChannelNamespace",
		} {
			for _, resource := range []string{apis, namespaces} {
				want[grant{action: action, resource: resource, condition: conditionJSON(t, s.TaggedByOcelForItsTier())}] = true
			}
		}
		granted := map[grant]bool{}
		for g := range grantsOf(t, document) {
			if strings.HasPrefix(g.action, "appsync:") {
				granted[g] = true
			}
		}
		if !maps.Equal(granted, want) {
			t.Errorf("the %s credential grants appsync %v, want exactly %v: an Event API and its namespaces created tagged, and touched only while tagged", purpose, granted, want)
		}
	}
}

func TestEveryCredentialKeepsRealtimeSigningKeysUnderTheirRootAlone(t *testing.T) {
	for purpose, document := range bothCredentials(t) {
		tier := tierOf(purpose)
		s := defaultNamespace.Scope(tier)
		keys := "arn:aws:secretsmanager:*:*:secret:" + defaultNamespace.SigningKeyRoot() + "/" + string(tier) + "/*"
		want := map[grant]bool{
			{action: "secretsmanager:CreateSecret", resource: keys, condition: conditionJSON(t, s.TaggedOnCreateOnlyForItsTier())}: true,
			{action: "secretsmanager:TagResource", resource: keys, condition: conditionJSON(t, s.TaggedOnCreateOnlyForItsTier())}:  true,
			{action: "secretsmanager:ListSecrets", resource: iam.AnyResource, condition: conditionJSON(t, nil)}:                    true,
		}
		for _, action := range []string{"secretsmanager:DeleteSecret", "secretsmanager:GetSecretValue"} {
			want[grant{action: action, resource: keys, condition: conditionJSON(t, s.TaggedByOcelForItsTier())}] = true
		}
		granted := map[grant]bool{}
		for g := range grantsOf(t, document) {
			if strings.HasPrefix(g.action, "secretsmanager:") && g.resource != iam.AppSecretARN {
				granted[g] = true
			}
		}
		if !maps.Equal(granted, want) {
			t.Errorf("the %s credential grants secretsmanager %v beyond the RDS master secrets, want exactly %v", purpose, granted, want)
		}
	}
}

func TestTheDeployCredentialCreatesTheAppRoleThroughTheDataAPIAndAnOperatorSignsInAsIt(t *testing.T) {
	t.Parallel()

	for name, document := range bothCredentials(t) {
		grants := grantsOf(t, document)
		var execute, connect []string
		for g := range grants {
			switch g.action {
			case "rds-data:ExecuteStatement":
				execute = append(execute, g.resource)
			case "rds-db:connect":
				connect = append(connect, g.resource)
			}
		}
		if !slices.Equal(execute, []string{productionScope.AppClusterARN()}) {
			t.Errorf("%s grants rds-data:ExecuteStatement on %v, want the clusters in the app scope alone: the deploy creates each cluster's app role through the Data API", name, execute)
		}
		if want := []string{"arn:aws:rds-db:*:*:dbuser:*/" + postgres.AppRoleFor(environment.TierProduction), "arn:aws:rds-db:*:*:dbuser:*/" + postgres.AppRoleFor(environment.TierPreview)}; len(connect) != 1 || !slices.Contains(want, connect[0]) {
			t.Errorf("%s grants rds-db:connect on %v, want %s alone: a port forward signs in as the app role, never as the master user", name, connect, want)
		}
	}
}
