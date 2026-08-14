package deploy

import (
	"testing"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

func TestRealizationFor(t *testing.T) {
	t.Parallel()

	pg := naming.TokenPostgres
	bucket := naming.TokenBucket

	cases := []struct {
		name      string
		rt        string
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
			t.Parallel()

			if got := realizationFor(tc.rt, tc.lifecycle); got != tc.want {
				t.Errorf("realizationFor(%v, %v) = %v, want %v", tc.rt, tc.lifecycle, got, tc.want)
			}
		})
	}
}
