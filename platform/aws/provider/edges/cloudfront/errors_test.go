package cloudfront

import (
	"errors"
	"strings"
	"testing"

	cftypes "github.com/aws/aws-sdk-go-v2/service/cloudfront/types"
)

func TestADistributionCloudFrontRefusesNamesTheQuotaThatRefusedIt(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		name  string
		err   error
		quota string
	}{
		{"account", &cftypes.TooManyDistributions{}, `"Distributions per AWS account"`},
		{"function", &cftypes.TooManyDistributionsWithFunctionAssociations{}, `"Distributions associated with the same function"`},
		{"cache policy", &cftypes.TooManyDistributionsAssociatedToCachePolicy{}, `"Distributions associated with the same cache policy"`},
		{"headers policy", &cftypes.TooManyDistributionsAssociatedToResponseHeadersPolicy{}, `"Distributions associated with the same response headers policy"`},
		{"origin access control", &cftypes.TooManyDistributionsAssociatedToOriginAccessControl{}, `"Distributions per origin access control"`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()

			err := createError("distribution", "ocel-shop-production", tc.err)
			if !strings.Contains(err.Error(), tc.quota) {
				t.Errorf("createError = %q, want it to name the %s quota", err, tc.quota)
			}
			if !errors.Is(err, tc.err) {
				t.Errorf("createError = %v, want it to wrap CloudFront's refusal", err)
			}
			if tc.name != "account" && strings.Contains(err.Error(), "Distributions per AWS account") {
				t.Errorf("createError = %q, which sends the user to raise a quota that does not refuse them", err)
			}
		})
	}
}

func TestATierAtItsSharedResolversLimitSaysRaisingAQuotaWillNotHelp(t *testing.T) {
	t.Parallel()

	for _, refused := range []error{&cftypes.TooManyDistributionsWithFunctionAssociations{}, &cftypes.TooManyDistributionsAssociatedToCachePolicy{}} {
		err := createError("distribution", "ocel-shop-production", refused)
		if !strings.Contains(err.Error(), "cannot be raised") {
			t.Errorf("createError = %q, want it to say AWS does not raise this quota", err)
		}
	}
}
