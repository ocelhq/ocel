package bucket

import (
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/platform/aws/provider/component"
)

func resourceNames(project, env, logicalName string) map[string]component.ResourceRef {
	at := component.ResourceCoordinate(project, env, logicalName, naming.KindBucket)
	completerRole := naming.ResourceID(at.Kind, at.Name, uploadCompleterLocalName+"-role")
	return map[string]component.ResourceRef{
		"bucket":                        {Token: component.TokenS3Bucket, Name: naming.ResourceID(at.Kind, at.Name)},
		"publicAccessBlock":             {Token: component.TokenS3PublicAccess, Name: naming.ResourceID(at.Kind, at.Name, "public-access-block")},
		"cors":                          {Token: component.TokenS3CORS, Name: naming.ResourceID(at.Kind, at.Name, "cors")},
		"uploadCompleterRole":           {Token: component.TokenIAMRole, Name: completerRole},
		"uploadCompleterS3Policy":       {Token: component.TokenIAMRolePolicy, Name: completerRole + "-policy-s3"},
		"uploadCompleterSessionsPolicy": {Token: component.TokenIAMRolePolicy, Name: completerRole + "-policy-sessions"},
		"uploadCompleterLogsPolicy":     {Token: component.TokenIAMRoleAttachment, Name: naming.ResourceID(at.Kind, at.Name, uploadCompleterLocalName+"-logs-policy")},
		"uploadCompleterLogGroup":       {Token: component.TokenLogGroup, Name: naming.ResourceID(at.Kind, at.Name, uploadCompleterLocalName+"-logs")},
		"uploadCompleter":               {Token: component.TokenLambdaFunction, Name: naming.ResourceID(at.Kind, at.Name, uploadCompleterLocalName)},
		"uploadCompleterPermission":     {Token: component.TokenLambdaPermission, Name: naming.ResourceID(at.Kind, at.Name, uploadCompleterLocalName+"-permission")},
		"notification":                  {Token: component.TokenS3Notification, Name: naming.ResourceID(at.Kind, at.Name, "notification")},
	}
}
