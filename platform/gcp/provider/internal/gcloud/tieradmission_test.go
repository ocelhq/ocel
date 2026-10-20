package gcloud

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

func TestATagThatNeverAdmitsTheCredentialEndsTheWaitNamingWhatGoogleStillRefuses(t *testing.T) {
	c := &Clients{Names: names.New("ocel", "acme-prod"), Region: "europe-west1"}
	denial := errors.New("the deploy credential lacks roles/cloudsql.admin, on the condition resource.matchTag('acme-prod/ocel-tier', 'production')")
	target := TaggedResource{Name: "//sqladmin.googleapis.com/projects/acme-prod/instances/orders", Admits: func(context.Context, environment.Tier) error {
		return notAdmitted{denial}
	}}

	err := c.awaitAdmitted(context.Background(), environment.TierProduction, target, Patience{Attempts: 2, Ceiling: time.Millisecond})

	if err == nil || !strings.Contains(err.Error(), denial.Error()) {
		t.Errorf("a wait that never admitted the credential ended with %v, want it to name what Google still refuses: %v", err, denial)
	}
}
