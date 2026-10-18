package registry

import (
	"github.com/ocelhq/ocel/pkg/images"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

const (
	principalProjectKey = "aws:PrincipalTag/" + ProjectTag
	principalProject    = "${" + principalProjectKey + "}"

	repositoryARN                 = "arn:aws:ecr:*:*:repository/" + Namespace + "/*"
	principalProjectRepositoryARN = "arn:aws:ecr:*:*:repository/" + Namespace + "/" + principalProject + images.ProjectSeparator + "*"
)

func principalUntagged() map[string]any {
	return map[string]any{"Null": map[string]any{principalProjectKey: "true"}}
}

func ofPrincipalProject() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"aws:ResourceTag/" + ProjectTag: principalProject}}
}

func taggedForPrincipalProject() map[string]any {
	return map[string]any{"StringEquals": map[string]any{"aws:RequestTag/" + ProjectTag: principalProject}}
}

func ofAProject() map[string]any {
	return map[string]any{"Null": map[string]any{"aws:ResourceTag/" + ProjectTag: "false"}}
}

func taggedForAProject() map[string]any {
	return map[string]any{"Null": map[string]any{"aws:RequestTag/" + ProjectTag: "false"}}
}

func Credential(iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"ecr:GetAuthorizationToken"},
			Resources: []string{iam.AnyResource},
		},
		{
			Actions: []string{
				"ecr:BatchCheckLayerAvailability",
				"ecr:BatchGetImage",
				"ecr:CompleteLayerUpload",
				"ecr:DescribeImages",
				"ecr:DescribeRepositories",
				"ecr:GetDownloadUrlForLayer",
				"ecr:InitiateLayerUpload",
				"ecr:ListTagsForResource",
				"ecr:PutImage",
				"ecr:UploadLayerPart",
			},
			Resources: []string{repositoryARN},
			Condition: iam.InCallerAccount(),
		},
		{
			Actions:   []string{"ecr:CreateRepository", "ecr:TagResource"},
			Resources: []string{principalProjectRepositoryARN},
			Condition: iam.MergeConditions(iam.InCallerAccount(), taggedForPrincipalProject()),
		},
		{
			Actions:   []string{"ecr:BatchDeleteImage", "ecr:DeleteRepository"},
			Resources: []string{principalProjectRepositoryARN},
			Condition: iam.MergeConditions(iam.InCallerAccount(), ofPrincipalProject()),
		},
		{
			Actions:   []string{"ecr:CreateRepository", "ecr:TagResource"},
			Resources: []string{repositoryARN},
			Condition: iam.MergeConditions(iam.InCallerAccount(), principalUntagged(), taggedForAProject()),
		},
		{
			Actions:   []string{"ecr:BatchDeleteImage", "ecr:DeleteRepository"},
			Resources: []string{repositoryARN},
			Condition: iam.MergeConditions(iam.InCallerAccount(), principalUntagged(), ofAProject()),
		},
	}
}
