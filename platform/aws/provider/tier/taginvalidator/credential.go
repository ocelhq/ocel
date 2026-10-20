package taginvalidator

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"iam:PassRole"},
			Resources: []string{s.TagInvalidatorRole},
			Condition: iam.PassedTo(iam.LambdaPrincipal),
		},
		{
			Actions:   []string{"lambda:CreateEventSourceMapping"},
			Resources: []string{iam.AnyResource},
			Condition: s.InvokesTheTagInvalidator(),
		},
		{
			Actions: []string{
				"lambda:DeleteEventSourceMapping",
				"lambda:GetEventSourceMapping",
				"lambda:UpdateEventSourceMapping",
			},
			Resources: []string{iam.EventSourceMappingARN},
			Condition: s.InvokesTheTagInvalidator(),
		},
		{
			Actions:   []string{"lambda:ListEventSourceMappings"},
			Resources: []string{iam.AnyResource},
		},
		{
			Actions:   []string{"sqs:CreateQueue", "sqs:TagQueue"},
			Resources: []string{s.TagInvalidatorQueue},
			Condition: s.TaggedOnCreateOnlyForItsTier(),
		},
		{
			Actions:   []string{"sqs:DeleteQueue", "sqs:GetQueueAttributes", "sqs:GetQueueUrl", "sqs:SetQueueAttributes"},
			Resources: []string{s.TagInvalidatorQueue},
			Condition: s.TaggedByOcelForItsTier(),
		},
	}
}
