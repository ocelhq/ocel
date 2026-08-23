package server

import (
	"testing"

	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
)

func TestClassOf(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		tier environmentv1.Tier
		want string
	}{
		{name: "an unspecified class is the production bootstrap", tier: environmentv1.Tier_TIER_UNSPECIFIED, want: bootstrap.ClassProduction},
		{name: "production", tier: environmentv1.Tier_TIER_PRODUCTION, want: bootstrap.ClassProduction},
		{name: "preview", tier: environmentv1.Tier_TIER_PREVIEW, want: bootstrap.ClassPreview},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			got, err := classOf(tc.tier)
			if err != nil {
				t.Fatalf("classOf(%v): %v", tc.tier, err)
			}
			if got != tc.want {
				t.Errorf("classOf(%v) = %q, want %q", tc.tier, got, tc.want)
			}
		})
	}

	t.Run("a class this build does not know is not a bootstrap", func(t *testing.T) {
		t.Parallel()

		if _, err := classOf(environmentv1.Tier(99)); err == nil {
			t.Error("a class naming no bootstrap, want a refusal")
		}
	})
}
