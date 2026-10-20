package bucket

import (
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

func resourceNames(at component.Stack, logicalName string) (map[string][]component.ResourceRef, error) {
	head, err := component.RandomTaggedHead(at.Namespace, component.ResourceCoordinate(at.Project, at.Env, logicalName, naming.KindBucket), component.MaxLambdaNameLen, uploadCompleterSuffix)
	if err != nil {
		return nil, err
	}
	bucket := component.NameOf(head)
	completer := bucket.Child(uploadCompleterSuffix)
	return map[string][]component.ResourceRef{
		"bucket":                        {{Token: component.TokenS3Bucket, Name: bucket.Pulumi}},
		"publicAccessBlock":             {{Token: component.TokenS3PublicAccess, Name: bucket.Part(publicAccessBlockPart)}},
		"cors":                          {{Token: component.TokenS3CORS, Name: bucket.Part(corsPart)}},
		"uploadCompleterRole":           {{Token: component.TokenIAMRole, Name: completer.Pulumi}},
		"uploadCompleterS3Policy":       {{Token: component.TokenIAMRolePolicy, Name: completer.Part(completerS3Policy)}},
		"uploadCompleterSessionsPolicy": {{Token: component.TokenIAMRolePolicy, Name: completer.Part(completerSessionsPolicy)}},
		"uploadCompleterLogsPolicy":     {{Token: component.TokenIAMRoleAttachment, Name: completer.Attachment(iam.LambdaBasicExecutionPolicyARN)}},
		"uploadCompleterLogGroup":       {{Token: component.TokenLogGroup, Name: component.LambdaLogGroupName(completer).Pulumi}},
		"uploadCompleter":               {{Token: component.TokenLambdaFunction, Name: completer.Pulumi}},
		"uploadCompleterPermission":     {{Token: component.TokenLambdaPermission, Name: completer.Part(completerInvokePermission)}},
		"notification":                  {{Token: component.TokenS3Notification, Name: bucket.Part(notificationPart)}},
	}, nil
}
