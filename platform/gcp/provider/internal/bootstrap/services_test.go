package bootstrap

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"slices"
	"testing"

	"google.golang.org/api/cloudresourcemanager/v1"

	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/googletest"
)

func servingServiceUsage(t *testing.T, usage *googletest.ServiceUsage) Engine {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !usage.Serve(w, r) {
			t.Errorf("called %s %s, want only Service Usage asked", r.Method, r.URL.Path)
			http.NotFound(w, r)
		}
	}))
	t.Cleanup(server.Close)
	return Engine{Clients: googletest.Clients(server.URL), ServiceStates: &ServiceStates{}}
}

func TestEveryServiceARunChecksIsReadInBatchesGoogleAcceptsAndNeverReadAgain(t *testing.T) {
	t.Parallel()

	var apis []string
	for i := range 35 {
		apis = append(apis, fmt.Sprintf("api%d.googleapis.com", i))
	}
	usage := &googletest.ServiceUsage{ServicesOff: []string{"api3.googleapis.com", "api33.googleapis.com"}}
	b := servingServiceUsage(t, usage)

	off, err := b.ListServicesOff(t.Context(), apis)
	if err != nil {
		t.Fatalf("ListServicesOff() = %v", err)
	}
	if want := []string{"api3.googleapis.com", "api33.googleapis.com"}; !slices.Equal(off, want) {
		t.Errorf("ListServicesOff() = %v, want %v", off, want)
	}
	if again, err := b.ListServicesOff(t.Context(), apis[30:]); err != nil || !slices.Equal(again, []string{"api33.googleapis.com"}) {
		t.Errorf("ListServicesOff() again = %v, %v, want api33.googleapis.com off", again, err)
	}
	if usage.BatchGets != 2 || usage.Gets != 0 {
		t.Errorf("read the services in %d batches and %d single reads, want 2 batches of at most 30 and nothing read twice", usage.BatchGets, usage.Gets)
	}
}

func switchingOn(t *testing.T, usage *googletest.ServiceUsage, withheld ...string) (Engine, *[]string) {
	t.Helper()
	var tested []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if usage.Serve(w, r) {
			return
		}
		if r.Method != http.MethodPost || r.URL.Path != "/v1/projects/"+googletest.Project+":testIamPermissions" {
			t.Errorf("called %s %s, want only Service Usage and testIamPermissions asked", r.Method, r.URL.Path)
			http.NotFound(w, r)
			return
		}
		asked := googletest.ReadBody[cloudresourcemanager.TestIamPermissionsRequest](w, r)
		if asked == nil {
			return
		}
		tested = append(tested, asked.Permissions...)
		googletest.WriteBody(w, &cloudresourcemanager.TestIamPermissionsResponse{Permissions: slices.DeleteFunc(asked.Permissions, func(permission string) bool {
			return slices.Contains(withheld, permission)
		})})
	}))
	t.Cleanup(server.Close)
	b := Engine{
		Clients:       googletest.Clients(server.URL),
		ServiceStates: &ServiceStates{},
		Principal:     func(context.Context) (string, error) { return "deployer@acme-prod.iam.gserviceaccount.com", nil },
	}
	return b, &tested
}

func TestServicesSwitchedOffAreSwitchedOnInBatchesGoogleAcceptsAndWaitedForUntilEachReportsOn(t *testing.T) {
	t.Parallel()

	var apis []string
	for i := range 25 {
		apis = append(apis, fmt.Sprintf("api%d.googleapis.com", i))
	}
	usage := &googletest.ServiceUsage{ServicesOff: slices.Clone(apis), Pending: 2, Propagating: 2}
	b, _ := switchingOn(t, usage)

	if err := b.SwitchOnServices(t.Context(), apis, nil); err != nil {
		t.Fatalf("SwitchOnServices() = %v", err)
	}

	if len(usage.Enabled) != 2 || len(usage.Enabled[0]) != 20 || !slices.Equal(usage.EnabledIDs(), apis) {
		t.Errorf("switched on %v, want all 25 in batches of at most 20", usage.Enabled)
	}
	if usage.Propagating != 0 {
		t.Errorf("returned while Google still reported %d services off, want it to wait until each reports on", usage.Propagating)
	}
	if off, err := b.ListServicesOff(t.Context(), apis); err != nil || len(off) > 0 {
		t.Errorf("ListServicesOff() after switching on = %v, %v, want none off", off, err)
	}
}

func TestSwitchingOnServicesThatAreAllOnAsksNothingOfTheCredential(t *testing.T) {
	t.Parallel()

	usage := &googletest.ServiceUsage{}
	b, tested := switchingOn(t, usage, servicesEnablePermission)

	if err := b.SwitchOnServices(t.Context(), BootstrapAPIs, nil); err != nil {
		t.Fatalf("SwitchOnServices() with every service on = %v, want nothing asked of a credential that may not switch services on", err)
	}
	if len(*tested) > 0 || len(usage.Enabled) > 0 {
		t.Errorf("tested %v and switched on %v, want nothing", *tested, usage.Enabled)
	}
}

func TestACredentialThatMayNotSwitchServicesOnIsRefusedOnceNamingEveryServiceOffWhoItIsAndBothRemedies(t *testing.T) {
	t.Parallel()

	usage := &googletest.ServiceUsage{ServicesOff: []string{"firestore.googleapis.com", "cloudkms.googleapis.com"}}
	b, _ := switchingOn(t, usage, servicesEnablePermission)

	err := b.SwitchOnServices(t.Context(), []string{"firestore.googleapis.com", "storage.googleapis.com", "cloudkms.googleapis.com"}, nil)

	var refused refusal.Refusal
	if !errors.As(err, &refused) || refused.Code != refusal.CodeDenied {
		t.Fatalf("SwitchOnServices() = %v, want a %s refusal", err, refusal.CodeDenied)
	}
	want := "project acme-prod has firestore.googleapis.com, cloudkms.googleapis.com switched off, and deployer@acme-prod.iam.gserviceaccount.com may not switch services on there.\n" +
		"Run `gcloud services enable firestore.googleapis.com cloudkms.googleapis.com --project acme-prod` once as someone who may, " +
		"or grant deployer@acme-prod.iam.gserviceaccount.com roles/serviceusage.serviceUsageAdmin on the project, then try again"
	if refused.Message != want {
		t.Errorf("refused with\n%s\nwant\n%s", refused.Message, want)
	}
	if len(usage.Enabled) > 0 {
		t.Errorf("switched on %v, want nothing", usage.Enabled)
	}
}

func TestAProjectWithNoBillingAccountIsRefusedWithWhereToLinkOne(t *testing.T) {
	t.Parallel()

	for name, usage := range map[string]*googletest.ServiceUsage{
		"in the operation": {ServicesOff: []string{"run.googleapis.com"}, Unbilled: true},
		"at once":          {ServicesOff: []string{"run.googleapis.com"}, UnbilledAtOnce: true},
	} {
		b, _ := switchingOn(t, usage)

		err := b.SwitchOnServices(t.Context(), []string{"run.googleapis.com"}, nil)

		var refused refusal.Refusal
		if !errors.As(err, &refused) || refused.Code != refusal.CodeNotReady {
			t.Fatalf("%s: SwitchOnServices() = %v, want a %s refusal", name, err, refusal.CodeNotReady)
		}
		want := "project acme-prod has no billing account linked, and Google switches on run.googleapis.com only in a project that bills.\n" +
			"Link one at https://console.cloud.google.com/billing/linkedaccount?project=acme-prod, then try again"
		if refused.Message != want {
			t.Errorf("%s: refused with\n%s\nwant\n%s", name, refused.Message, want)
		}
	}
}

func TestEveryResourceABootstrapOrItsPartsReadsIsHostedByAServiceItChecks(t *testing.T) {
	t.Parallel()

	tier := environment.TierProduction
	for _, item := range Resources(googletest.Names, tier, false) {
		if !slices.Contains(BootstrapAPIs, item.api()) {
			t.Errorf("the base reads %s through %q, which a bootstrap never checks is on", item.ID(), item.api())
		}
	}
	for _, part := range DeployedParts() {
		for _, item := range part.Items(googletest.Names, tier) {
			if !slices.Contains(BootstrapAPIs, item.api()) && !slices.Contains(part.APIs, item.api()) {
				t.Errorf("the %s part reads %s through %q, which neither it nor the base checks is on", part.Name, item.ID(), item.api())
			}
		}
	}
}
