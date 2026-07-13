package deploy

import (
	"testing"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

func TestRealizationFor(t *testing.T) {
	pg := resourcesv1.ResourceType_RESOURCE_TYPE_POSTGRES
	bucket := resourcesv1.ResourceType_RESOURCE_TYPE_BUCKET

	cases := []struct {
		name      string
		rt        resourcesv1.ResourceType
		lifecycle deploymentsv1.Environment_Lifecycle
		want      Realization
	}{
		{"postgres ephemeral is sliced", pg, deploymentsv1.Environment_LIFECYCLE_EPHEMERAL, RealizationLogicalSlice},
		{"postgres persistent is real", pg, deploymentsv1.Environment_LIFECYCLE_PERSISTENT, RealizationReal},
		{"postgres unspecified is real", pg, deploymentsv1.Environment_LIFECYCLE_UNSPECIFIED, RealizationReal},
		{"bucket ephemeral is real", bucket, deploymentsv1.Environment_LIFECYCLE_EPHEMERAL, RealizationReal},
		{"bucket persistent is real", bucket, deploymentsv1.Environment_LIFECYCLE_PERSISTENT, RealizationReal},
		{"bucket unspecified is real", bucket, deploymentsv1.Environment_LIFECYCLE_UNSPECIFIED, RealizationReal},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := realizationFor(tc.rt, tc.lifecycle); got != tc.want {
				t.Errorf("realizationFor(%v, %v) = %v, want %v", tc.rt, tc.lifecycle, got, tc.want)
			}
		})
	}
}
