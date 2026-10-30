package credential

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Bucket(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"s3:CreateBucket", "s3:TagResource"},
			Resources: []string{s.AppBucketARN()},
			Condition: iam.MergeConditions(iam.InCallerAccount(), s.TaggedOnCreateForItsTier(), s.UnevaluatedOrTaggedForItsTier()),
		},
		{
			Actions: []string{
				"s3:GetAccelerateConfiguration",
				"s3:GetBucket*",
				"s3:GetEncryptionConfiguration",
				"s3:GetLifecycleConfiguration",
				"s3:GetReplicationConfiguration",
				"s3:ListBucket",
				"s3:ListTagsForResource",
				"s3:PutBucketAbac",
			},
			Resources: []string{s.AppBucketARN()},
			Condition: iam.MergeConditions(iam.InCallerAccount(), s.UnevaluatedOrTaggedForItsTier()),
		},
		{
			Actions: []string{
				"s3:DeleteBucket",
				"s3:PutBucketCORS",
				"s3:PutBucketNotification",
				"s3:PutBucketPublicAccessBlock",
				"s3:UntagResource",
			},
			Resources: []string{s.AppBucketARN()},
			Condition: iam.MergeConditions(iam.InCallerAccount(), s.TaggedForItsTier()),
		},
	}
}
