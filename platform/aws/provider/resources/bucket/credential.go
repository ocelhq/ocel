package bucket

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func credential(iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"s3:CreateBucket"},
			Resources: []string{iam.AppBucketARN},
			Condition: iam.InCallerAccount(),
		},
		{
			Actions: []string{
				"s3:DeleteBucket",
				"s3:GetAccelerateConfiguration",
				"s3:GetBucket*",
				"s3:GetEncryptionConfiguration",
				"s3:GetLifecycleConfiguration",
				"s3:GetReplicationConfiguration",
				"s3:PutBucketCORS",
				"s3:PutBucketNotification",
				"s3:PutBucketPublicAccessBlock",
				"s3:PutBucketTagging",
			},
			Resources: []string{iam.AppBucketARN},
			Condition: iam.InCallerAccount(),
		},
	}
}
