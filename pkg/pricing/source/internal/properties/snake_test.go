package properties_test

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/pricing/source/internal/properties"
)

func TestCamelCaseBecomesSnakeCase(t *testing.T) {
	for in, want := range map[string]string{
		"memorySize":                       "memory_size",
		"serverlessv2ScalingConfiguration": "serverlessv2_scaling_configuration",
		"ipv6CidrBlock":                    "ipv6_cidr_block",
		"URLMap":                           "url_map",
		"minInstanceCount":                 "min_instance_count",
		"region":                           "region",
	} {
		if got := properties.Snake(in); got != want {
			t.Errorf("Snake(%q) = %q, want %q", in, got, want)
		}
	}
}
