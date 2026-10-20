package certs

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Credential(s iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"acm:RequestCertificate"},
			Resources: []string{iam.AnyResource},
			Condition: s.TaggedOnCreateForItsTier(),
		},
		{
			Actions: []string{
				"acm:AddTagsToCertificate",
				"acm:DeleteCertificate",
				"acm:ListTagsForCertificate",
				"acm:RemoveTagsFromCertificate",
			},
			Resources: []string{iam.AppCertificateARN},
			Condition: s.TaggedByOcelForItsTier(),
		},
		{
			Actions:   []string{"acm:DescribeCertificate"},
			Resources: []string{iam.AppCertificateARN},
		},
		{
			Actions:   []string{"acm:ListCertificates"},
			Resources: []string{iam.AnyResource},
		},
	}
}
