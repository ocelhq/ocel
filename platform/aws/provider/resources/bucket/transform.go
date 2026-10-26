package bucket

import (
	"github.com/pulumi/pulumi/sdk/v3/go/pulumi"

	"github.com/ocelhq/ocel/pkg/naming"
	ocelpulumi "github.com/ocelhq/ocel/pkg/provider/pulumi"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
	"github.com/ocelhq/ocel/platform/aws/provider/iam"
)

type Names struct {
	Bucket    component.Name
	Completer component.Name
}

func NamesFor(at component.Stack, logicalName string) (Names, error) {
	untagged, err := component.UntaggedName(at.Namespace, component.ResourceCoordinate(at.Project, at.Env, logicalName, naming.KindBucket), component.MaxLambdaNameLen, uploadCompleterSuffix)
	if err != nil {
		return Names{}, err
	}
	return namesOf(component.Name{Pulumi: untagged}), nil
}

func (n Names) WithRandomTag(ctx *pulumi.Context) (Names, error) {
	bucket, err := component.RandomTaggedName(ctx, n.Bucket.Pulumi)
	if err != nil {
		return Names{}, err
	}
	return namesOf(bucket), nil
}

func namesOf(bucket component.Name) Names {
	return Names{Bucket: bucket, Completer: bucket.Child(uploadCompleterSuffix)}
}

func (n Names) Surfaces() map[string][]ocelpulumi.ResourceRef {
	return map[string][]ocelpulumi.ResourceRef{
		"bucket":                        {n.bucket()},
		"publicAccessBlock":             {{Token: component.TokenS3PublicAccess, Name: n.Bucket.Part(publicAccessBlockPart)}},
		"cors":                          {{Token: component.TokenS3CORS, Name: n.Bucket.Part(corsPart)}},
		"uploadCompleterRole":           {{Token: component.TokenIAMRole, Name: n.Completer.Pulumi}},
		"uploadCompleterS3Policy":       {{Token: component.TokenIAMRolePolicy, Name: n.Completer.Part(completerS3Policy)}},
		"uploadCompleterSessionsPolicy": {{Token: component.TokenIAMRolePolicy, Name: n.Completer.Part(completerSessionsPolicy)}},
		"uploadCompleterLogsPolicy":     {{Token: component.TokenIAMRoleAttachment, Name: n.Completer.Attachment(iam.LambdaBasicExecutionPolicyARN)}},
		"uploadCompleterLogGroup":       {n.completerLogGroup()},
		"uploadCompleter":               {n.completer()},
		"uploadCompleterPermission":     {{Token: component.TokenLambdaPermission, Name: n.Completer.Part(completerInvokePermission)}},
		"notification":                  {{Token: component.TokenS3Notification, Name: n.Bucket.Part(notificationPart)}},
	}
}

func (n Names) bucket() ocelpulumi.ResourceRef {
	return ocelpulumi.ResourceRef{Token: component.TokenS3Bucket, Name: n.Bucket.Pulumi}
}

func (n Names) completer() ocelpulumi.ResourceRef {
	return ocelpulumi.ResourceRef{Token: component.TokenLambdaFunction, Name: n.Completer.Pulumi}
}

func (n Names) completerLogGroup() ocelpulumi.ResourceRef {
	return ocelpulumi.ResourceRef{Token: component.TokenLogGroup, Name: component.LambdaLogGroupName(n.Completer).Pulumi}
}
