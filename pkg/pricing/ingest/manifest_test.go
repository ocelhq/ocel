package ingest_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/pricing/ingest"
)

func TestAServiceKeepsAProductOnlyWhenEveryFilterOfItsFamilyHolds(t *testing.T) {
	ec2 := ingest.Service{
		Service:         "AmazonEC2",
		ProductFamilies: []string{"Compute Instance", "NAT Gateway"},
		Where: []ingest.Filter{
			{Attribute: "tenancy", In: []string{"Shared"}, ProductFamilies: []string{"Compute Instance"}},
		},
		Exclude: []ingest.Filter{
			{Attribute: "locationType", In: []string{"AWS Local Zone"}},
		},
	}
	for name, tc := range map[string]struct {
		family     string
		attributes map[string]string
		kept       bool
	}{
		"shared instance":          {"Compute Instance", map[string]string{"tenancy": "Shared", "locationType": "AWS Region"}, true},
		"dedicated instance":       {"Compute Instance", map[string]string{"tenancy": "Dedicated"}, false},
		"instance with no tenancy": {"Compute Instance", map[string]string{}, false},
		"nat gateway":              {"NAT Gateway", map[string]string{"locationType": "AWS Region"}, true},
		"local zone nat gateway":   {"NAT Gateway", map[string]string{"locationType": "AWS Local Zone"}, false},
		"another family":           {"Storage", map[string]string{"tenancy": "Shared"}, false},
	} {
		t.Run(name, func(t *testing.T) {
			if got := ec2.Keeps(tc.family, tc.attributes); got != tc.kept {
				t.Errorf("Keeps() = %v, want %v", got, tc.kept)
			}
		})
	}
	if !(ingest.Service{Service: "AWSLambda"}).Keeps("Serverless", nil) {
		t.Error("a service with no filters dropped a product")
	}
}
