package gcloud_test

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

const userProjectDenied = `{"error":{"code":403,"status":"PERMISSION_DENIED",` +
	`"message":"Caller does not have required permission to use project acme-prod.",` +
	`"details":[{"@type":"type.googleapis.com/google.rpc.ErrorInfo","reason":"USER_PROJECT_DENIED","domain":"googleapis.com",` +
	`"metadata":{"consumer":"projects/acme-prod","service":"serviceusage.googleapis.com"}}]}}`

func answeringUserProjectDenied(t *testing.T) *[]string {
	t.Helper()
	billed := []string{}
	googletest.ServeAsGoogle(t, func(w http.ResponseWriter, r *http.Request) {
		billed = append(billed, r.Header.Get(googletest.QuotaProjectHeader))
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusForbidden)
		_, _ = w.Write([]byte(userProjectDenied))
	})
	return &billed
}

func TestAUserCredentialThatMayNotBillTheProjectIsRefusedNamingWhatToGrantOrWhereElseToBill(t *testing.T) {
	googletest.UseUserCredential(t)
	answeringUserProjectDenied(t)
	c := &gcloud.Clients{Names: googletest.Names, Region: googletest.Region}

	err := c.RefuseBillingDenied(context.Background())

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied {
		t.Fatalf("RefuseBillingDenied() = %v, want a %s refusal", err, refusal.CodeDenied)
	}
	for _, named := range []string{"acme-prod", "serviceusage.services.use", "roles/serviceusage.serviceUsageConsumer", "GOOGLE_CLOUD_QUOTA_PROJECT"} {
		if !strings.Contains(refused.Message, named) {
			t.Errorf("refused with %q, want it to name %s", refused.Message, named)
		}
	}
}

func TestACredentialThatBillsNoProjectIsNotAskedWhetherItMay(t *testing.T) {
	googletest.UseUserCredential(t)
	billed := answeringUserProjectDenied(t)
	c := &gcloud.Clients{Names: googletest.Names, Region: googletest.Region, Endpoint: "http://127.0.0.1:1"}

	if err := c.RefuseBillingDenied(context.Background()); err != nil || len(*billed) != 0 {
		t.Errorf("RefuseBillingDenied() = %v after %d calls, want no call and no refusal: a client that names no quota project bills no one", err, len(*billed))
	}
}
