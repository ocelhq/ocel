package deploy

import (
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/environment/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
)

func TestRealizationFor(t *testing.T) {
	t.Parallel()

	pg := linksv1.LinkType_LINK_TYPE_POSTGRES
	bucket := linksv1.LinkType_LINK_TYPE_BUCKET

	cases := []struct {
		name      string
		rt        linksv1.LinkType
		lifecycle environmentv1.Lifecycle
		want      Realization
	}{
		{"postgres ephemeral is sliced", pg, environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL, RealizationLogicalSlice},
		{"postgres persistent is real", pg, environmentv1.Lifecycle_LIFECYCLE_PERSISTENT, RealizationReal},
		{"postgres unspecified is real", pg, environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED, RealizationReal},
		{"bucket ephemeral is real", bucket, environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL, RealizationReal},
		{"bucket persistent is real", bucket, environmentv1.Lifecycle_LIFECYCLE_PERSISTENT, RealizationReal},
		{"bucket unspecified is real", bucket, environmentv1.Lifecycle_LIFECYCLE_UNSPECIFIED, RealizationReal},
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
