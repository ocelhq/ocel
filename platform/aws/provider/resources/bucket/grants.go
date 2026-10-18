package bucket

import (
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
)

const (
	s3ARNPrefix = "arn:aws:s3:::"
)

func Grants(bucket string, sessions Sessions) []*variablestorev1.Grant {
	arn := s3ARNPrefix + bucket
	return []*variablestorev1.Grant{
		{
			Label: "objects",
			Actions: []string{
				"s3:AbortMultipartUpload",
				"s3:DeleteObject",
				"s3:GetObject",
				"s3:ListMultipartUploadParts",
				"s3:PutObject",
				"s3:PutObjectTagging",
			},
			Resources: []string{arn + "/*"},
		},
		{
			Label:     "listing",
			Actions:   []string{"s3:ListBucket", "s3:ListBucketMultipartUploads"},
			Resources: []string{arn},
		},
		{
			Label:     "sessions",
			Actions:   []string{"dynamodb:GetItem", "dynamodb:PutItem"},
			Resources: []string{sessions.TableARN},
			Conditions: []*variablestorev1.GrantCondition{{
				Operator: "ForAllValues:StringLike",
				Key:      "dynamodb:LeadingKeys",
				Values:   []string{sessions.KeyPrefix + "*"},
			}},
		},
	}
}
