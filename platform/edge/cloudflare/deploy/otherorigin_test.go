package cloudflare

import (
	"errors"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/refusal"
)

func TestAHostnameAnotherOriginHoldsIsRefusedAsInvalidNamingThatOrigin(t *testing.T) {
	t.Parallel()

	for _, tc := range []struct {
		hostname, subject string
	}{
		{hostname: "shop.app.com", subject: "one hostname belongs to one origin"},
		{hostname: "*.preview.app.com", subject: "one preview base domain belongs to one origin"},
	} {
		err := refuseOtherOrigin(tc.hostname+" is held", tc.hostname, otherOrigin)
		if !isOtherOriginRefusal(err, otherOrigin) || !strings.Contains(err.Error(), tc.subject) || !strings.Contains(err.Error(), "release "+tc.hostname+" on "+otherOrigin.String()) {
			t.Errorf("refuseOtherOrigin(%q) = %v, want an invalid refusal saying %q and to release it on %s", tc.hostname, err, tc.subject, otherOrigin)
		}
	}
}

func isOtherOriginRefusal(err error, held OriginScope) bool {
	var refused refusal.Refusal
	return errors.As(err, &refused) && refused.Code == refusal.CodeInvalid &&
		strings.Contains(refused.Message, held.String()) && strings.Contains(refused.Message, "belongs to one origin")
}
