package account

import (
	"slices"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

func policyARN(name string) string { return "arn:aws:iam::*:policy/" + name }

func aliasARN(alias string) string { return "arn:aws:kms:*:*:" + alias }

func taggedAsItsVariablesKey(s iam.Scope) map[string]any {
	return map[string]any{"StringEquals": map[string]any{
		"aws:ResourceTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue,
		"aws:ResourceTag/" + naming.EnvTierTagKey:        string(s.Tier),
	}}
}

func variablesKeyLifecycleActions() []string {
	return []string{
		"kms:CancelKeyDeletion",
		"kms:DescribeKey",
		"kms:DisableKey",
		"kms:DisableKeyRotation",
		"kms:EnableKey",
		"kms:EnableKeyRotation",
		"kms:GetKeyPolicy",
		"kms:GetKeyRotationStatus",
		"kms:ListResourceTags",
		"kms:PutKeyPolicy",
		"kms:ScheduleKeyDeletion",
		"kms:UntagResource",
	}
}

func AccessCredential(s iam.Scope) []iam.Statement {
	return slices.Concat([]iam.Statement{
		{
			Actions: []string{
				"s3:AbortMultipartUpload",
				"s3:DeleteObject",
				"s3:GetObject",
				"s3:GetObjectTagging",
				"s3:ListMultipartUploadParts",
				"s3:PutObject",
			},
			Resources: s.BootstrapObjects,
			Condition: iam.InCallerAccount(),
		},
		{
			Actions:   []string{"s3:GetBucketLocation", "s3:ListBucket", "s3:ListBucketMultipartUploads"},
			Resources: s.BootstrapBuckets,
			Condition: iam.InCallerAccount(),
		},
		{
			Actions: []string{
				"dynamodb:BatchGetItem",
				"dynamodb:BatchWriteItem",
				"dynamodb:DeleteItem",
				"dynamodb:DescribeTable",
				"dynamodb:GetItem",
				"dynamodb:PutItem",
				"dynamodb:Query",
				"dynamodb:UpdateItem",
			},
			Resources: s.BootstrapTables,
		},
		{
			Actions:   []string{"ssm:GetParameter", "ssm:GetParameters"},
			Resources: slices.Concat([]string{s.PassphraseParameter, s.OriginParameter}, s.EdgeParameters),
		},
		{
			Actions:   []string{"kms:Decrypt", "kms:DescribeKey", "kms:Encrypt", "kms:GenerateDataKey"},
			Resources: []string{iam.AnyKeyARN},
			Condition: iam.MergeConditions(s.AliasedAsItsVariablesKey(), map[string]any{
				"StringEquals": map[string]any{"aws:ResourceTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue},
			}),
		},
		{
			Actions:   []string{"kms:CreateGrant"},
			Resources: []string{iam.AnyKeyARN},
			Condition: iam.MergeConditions(s.AliasedAsItsVariablesKey(), map[string]any{
				"StringEquals": map[string]any{"aws:ResourceTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue},
				"Bool":         map[string]any{"kms:GrantIsForAWSResource": "true"},
			}),
		},
		{
			Actions:   []string{"cloudformation:DescribeStacks", "cloudformation:ListStackResources"},
			Resources: s.BootstrapStacks,
		},
		{
			Actions:   []string{"ec2:DescribeRegions", "sts:GetCallerIdentity"},
			Resources: []string{iam.AnyResource},
		},
	}, broughtVariablesKeyAccess(s.BroughtVariablesKey))
}

func broughtVariablesKeyAccess(broughtKey string) []iam.Statement {
	if broughtKey == "" {
		return nil
	}
	return []iam.Statement{
		{
			Actions:   []string{"kms:Decrypt", "kms:DescribeKey", "kms:Encrypt"},
			Resources: []string{broughtKey},
		},
		{
			Actions:   []string{"kms:CreateGrant"},
			Resources: []string{broughtKey},
			Condition: map[string]any{
				"Bool": map[string]any{"kms:GrantIsForAWSResource": "true"},
			},
		},
	}
}

func ProvisionCredential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions: []string{
				"cloudformation:CreateChangeSet",
				"cloudformation:CreateStack",
				"cloudformation:DeleteStack",
				"cloudformation:DescribeStackEvents",
			},
			Resources: s.BootstrapStacks,
		},
		{
			Actions: []string{
				"cloudformation:DeleteChangeSet",
				"cloudformation:DescribeChangeSet",
				"cloudformation:ExecuteChangeSet",
			},
			Resources: slices.Concat(s.BootstrapStacks, s.BootstrapChangeSets),
		},
		{
			Actions:   []string{"s3:CreateBucket"},
			Resources: s.BootstrapBuckets,
		},
		{
			Actions: []string{
				"s3:DeleteBucket",
				"s3:DeleteBucketPolicy",
				"s3:GetBucket*",
				"s3:GetEncryptionConfiguration",
				"s3:GetLifecycleConfiguration",
				"s3:ListBucketVersions",
				"s3:PutBucketPolicy",
				"s3:ListTagsForResource",
				"s3:PutBucketAbac",
				"s3:PutBucketPublicAccessBlock",
				"s3:PutBucketVersioning",
				"s3:PutEncryptionConfiguration",
				"s3:PutLifecycleConfiguration",
				"s3:TagResource",
				"s3:UntagResource",
			},
			Resources: s.BootstrapBuckets,
			Condition: iam.InCallerAccount(),
		},
		{
			Actions:   []string{"s3:DeleteObjectVersion", "s3:GetObjectVersion"},
			Resources: s.BootstrapObjects,
			Condition: iam.InCallerAccount(),
		},
		{
			Actions: []string{
				"dynamodb:CreateTable",
				"dynamodb:DeleteTable",
				"dynamodb:DescribeContinuousBackups",
				"dynamodb:DescribeStream",
				"dynamodb:DescribeTimeToLive",
				"dynamodb:ListStreams",
				"dynamodb:ListTagsOfResource",
				"dynamodb:TagResource",
				"dynamodb:UntagResource",
				"dynamodb:UpdateContinuousBackups",
				"dynamodb:UpdateTable",
				"dynamodb:UpdateTimeToLive",
			},
			Resources: s.BootstrapTables,
		},
		{
			Actions:   []string{"kms:CreateKey"},
			Resources: []string{iam.AnyResource},
			Condition: map[string]any{"StringEquals": map[string]any{
				"aws:RequestTag/" + VariablesKeyComponentTagKey: VariablesKeyComponentTagValue,
				"aws:RequestTag/" + naming.EnvTierTagKey:        string(s.Tier),
			}},
		},
		{
			Actions:   variablesKeyLifecycleActions(),
			Resources: []string{iam.AnyKeyARN},
			Condition: taggedAsItsVariablesKey(s),
		},
		{
			Actions:   []string{"kms:TagResource"},
			Resources: []string{iam.AnyKeyARN},
			Condition: iam.MergeConditions(taggedAsItsVariablesKey(s), s.RetaggedOnlyAsItsTier()),
		},
		{
			Actions:   []string{"kms:DescribeKey", "kms:GetKeyPolicy", "kms:GetKeyRotationStatus", "kms:ListResourceTags"},
			Resources: []string{iam.AnyKeyARN},
			Condition: s.AliasedAsItsVariablesKey(),
		},
		{
			Actions:   []string{"kms:CreateAlias", "kms:DeleteAlias", "kms:UpdateAlias"},
			Resources: []string{aliasARN(s.VariablesKeyAlias)},
		},
		{
			Actions:   []string{"kms:CreateAlias", "kms:DeleteAlias", "kms:UpdateAlias"},
			Resources: []string{iam.AnyKeyARN},
			Condition: taggedAsItsVariablesKey(s),
		},
		{
			Actions: []string{
				"iam:CreatePolicy",
				"iam:CreatePolicyVersion",
				"iam:DeletePolicy",
				"iam:DeletePolicyVersion",
				"iam:GetPolicy",
				"iam:GetPolicyVersion",
				"iam:ListEntitiesForPolicy",
				"iam:ListPolicyTags",
				"iam:ListPolicyVersions",
				"iam:TagPolicy",
				"iam:UntagPolicy",
			},
			Resources: []string{s.AppBoundaryPolicy},
		},
		{
			Actions:   []string{"iam:DeleteRolePermissionsBoundary"},
			Resources: s.AppBoundaryRoles(),
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"iam:UpdateAssumeRolePolicy"},
			Resources: s.PartRoles,
			Condition: iam.MergeConditions(s.TaggedForItsTier(), s.WithinAppBoundary()),
		},
		{
			Actions:   []string{"iam:CreateRole"},
			Resources: s.BootstrapRoles,
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions:   []string{"iam:TagRole"},
			Resources: s.BootstrapRoles,
			Condition: s.TaggedOnlyAsItsTier(),
		},
		{
			Actions: []string{
				"iam:GetRole",
				"iam:GetRolePolicy",
				"iam:ListAttachedRolePolicies",
				"iam:ListRolePolicies",
				"iam:ListRoleTags",
			},
			Resources: s.BootstrapRoles,
		},
		{
			Actions: []string{
				"iam:DeleteRole",
				"iam:DeleteRolePolicy",
				"iam:PutRolePolicy",
				"iam:UntagRole",
				"iam:UpdateAssumeRolePolicy",
				"iam:UpdateRole",
			},
			Resources: s.BootstrapRoles,
			Condition: s.TaggedForItsTier(),
		},
		{
			Actions:   []string{"iam:AttachRolePolicy", "iam:DetachRolePolicy"},
			Resources: s.BootstrapRoles,
			Condition: iam.MergeConditions(iam.AttachedPolicyIsAServiceRole(), s.TaggedForItsTier()),
		},
		{
			Actions:   []string{"ssm:AddTagsToResource", "ssm:PutParameter"},
			Resources: slices.Concat([]string{s.PassphraseParameter, s.OriginParameter}, s.EdgeParameters),
		},
		{
			Actions:   []string{"ssm:DeleteParameter", "ssm:DeleteParameters"},
			Resources: slices.Concat([]string{s.OriginParameter}, s.EdgeParameters),
		},
		{
			Actions:   []string{"ssm:DeleteParameter"},
			Resources: []string{s.PassphraseParameter},
		},
	}
}

func EdgePrincipalCredential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions: []string{
				"iam:CreateAccessKey",
				"iam:CreateUser",
				"iam:DeleteAccessKey",
				"iam:DeleteUser",
				"iam:DeleteUserPolicy",
				"iam:GetAccessKeyLastUsed",
				"iam:GetUser",
				"iam:GetUserPolicy",
				"iam:ListAccessKeys",
				"iam:ListAttachedUserPolicies",
				"iam:ListGroupsForUser",
				"iam:ListUserPolicies",
				"iam:ListUserTags",
				"iam:PutUserPolicy",
				"iam:UpdateAccessKey",
			},
			Resources: []string{s.EdgeUser},
		},
	}
}

func RuntimeLayerCredential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"lambda:GetLayerVersion", "lambda:PublishLayerVersion"},
			Resources: slices.Concat(s.RuntimeLayers, s.RuntimeLayerVersions),
		},
		{
			Actions:   []string{"lambda:ListLayerVersions"},
			Resources: []string{iam.AnyResource},
		},
	}
}

func RuntimeLayerReclaimCredential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"lambda:DeleteLayerVersion"},
			Resources: slices.Concat(s.RuntimeLayers, s.RuntimeLayerVersions),
		},
	}
}

func ConsoleTrustCredential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions: []string{
				"iam:CreatePolicy",
				"iam:CreatePolicyVersion",
				"iam:DeletePolicy",
				"iam:DeletePolicyVersion",
				"iam:GetPolicy",
				"iam:GetPolicyVersion",
				"iam:ListEntitiesForPolicy",
				"iam:ListPolicyTags",
				"iam:ListPolicyVersions",
				"iam:TagPolicy",
				"iam:UntagPolicy",
			},
			Resources: []string{s.DeployPolicies},
		},
		{
			Actions:   []string{"iam:AttachRolePolicy", "iam:DetachRolePolicy"},
			Resources: s.BootstrapRoles,
			Condition: iam.MergeConditions(s.TaggedForItsTier(), map[string]any{"ArnLike": map[string]any{"iam:PolicyARN": s.DeployPolicies}}),
		},
		{
			Actions:   []string{"iam:GetOpenIDConnectProvider"},
			Resources: []string{identityProviderARNPrefix + "*"},
		},
		{
			Actions:   []string{"iam:CreateOpenIDConnectProvider", "iam:TagOpenIDConnectProvider"},
			Resources: []string{identityProviderARNPrefix + "*"},
			Condition: map[string]any{"StringEquals": map[string]any{"aws:RequestTag/" + identityProviderComponentTagKey: identityProviderComponentTagValue}},
		},
		{
			Actions: []string{
				"iam:AddClientIDToOpenIDConnectProvider",
				"iam:DeleteOpenIDConnectProvider",
				"iam:RemoveClientIDFromOpenIDConnectProvider",
			},
			Resources: []string{identityProviderARNPrefix + "*"},
			Condition: map[string]any{"StringEquals": map[string]any{"aws:ResourceTag/" + identityProviderComponentTagKey: identityProviderComponentTagValue}},
		},
	}
}
