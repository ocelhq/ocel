package dns

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Credential(iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"route53:ListHostedZones"},
			Resources: []string{iam.AnyResource},
		},
		{
			Actions:   []string{"route53:ListResourceRecordSets"},
			Resources: []string{iam.HostedZoneARN},
		},
		{
			Actions:   []string{"route53:ChangeResourceRecordSets"},
			Resources: []string{iam.HostedZoneARN},
			Condition: map[string]any{
				"ForAllValues:StringEquals": map[string]any{
					"route53:ChangeResourceRecordSetsActions":     []string{"UPSERT", "DELETE"},
					"route53:ChangeResourceRecordSetsRecordTypes": []string{"A", "AAAA", "CNAME"},
				},
			},
		},
	}
}
