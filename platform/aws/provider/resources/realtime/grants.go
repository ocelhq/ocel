package realtime

import (
	variablestorev1 "github.com/ocelhq/ocel/pkg/proto/provider/variablestore/v1"
)

func realtimeGrants(apiARN, namespace string) []*variablestorev1.Grant {
	return []*variablestorev1.Grant{
		{Label: "connect", Actions: []string{"appsync:EventConnect"}, Resources: []string{apiARN}},
		{Label: "publish", Actions: []string{"appsync:EventPublish"}, Resources: []string{apiARN + "/channelNamespace/" + namespace}},
	}
}

const (
	widestEventAPIARN = "arn:aws:appsync:ap-southeast-5:123456789012:apis/abcdefghijklmnopqrstuvwxyz"
)
