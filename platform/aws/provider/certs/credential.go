package certs

import "github.com/ocelhq/ocel/platform/aws/provider/iam"

func Credential(iam.Scope) []iam.Statement {
	return []iam.Statement{
		{
			Actions:   []string{"acm:RequestCertificate"},
			Resources: []string{iam.AnyResource},
			Condition: iam.TaggedOnCreate(),
		},
		{
			Actions: []string{
				"acm:AddTagsToCertificate",
				"acm:DeleteCertificate",
				"acm:ListTagsForCertificate",
				"acm:RemoveTagsFromCertificate",
			},
			Resources: []string{iam.AppCertificateARN},
			Condition: iam.TaggedByOcel(),
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
