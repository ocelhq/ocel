package gcp

import (
	"slices"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
)

func TestABootstrapChecksItMayMakeTheTierTagAndBindEveryKindItTags(t *testing.T) {
	t.Parallel()

	for _, permission := range []string{
		"resourcemanager.tagKeys.create",
		"resourcemanager.tagKeys.get",
		"resourcemanager.tagValues.create",
		"resourcemanager.tagValues.get",
		"resourcemanager.tagValueBindings.create",
		"resourcemanager.tagValueBindings.delete",
		"storage.buckets.createTagBinding",
		"storage.buckets.listTagBindings",
		"iam.serviceAccounts.createTagBinding",
		"iam.serviceAccounts.listTagBindings",
		"iam.serviceAccounts.deleteTagBinding",
		"artifactregistry.repositories.createTagBinding",
		"artifactregistry.repositories.listTagBindings",
		"secretmanager.secrets.createTagBinding",
		"secretmanager.secrets.listTagBindings",
		"secretmanager.secrets.deleteTagBinding",
		"run.services.createTagBinding",
		"run.services.listTagBindings",
		"datastore.databases.listTagBindings",
		"datastore.databases.deleteTagBinding",
	} {
		if !slices.Contains(bootstrap.PermissionsFor(nil), permission) {
			t.Errorf("a bootstrap checks %v, want %s among them: the apply would stop on the tag it lacks", bootstrap.PermissionsFor(nil), permission)
		}
	}
	for _, role := range []string{"roles/resourcemanager.tagAdmin", "roles/resourcemanager.tagUser"} {
		if !slices.Contains(bootstrap.BootstrapRoles(), role) {
			t.Errorf("a bootstrap credential is granted %v, want %s", bootstrap.BootstrapRoles(), role)
		}
	}
	if !slices.Contains(bootstrap.BootstrapAPIs, "cloudresourcemanager.googleapis.com") {
		t.Errorf("a bootstrap checks %v are on, want the Resource Manager API every tag call goes to", bootstrap.BootstrapAPIs)
	}
}

func TestADeployCredentialForNoTierIsRefused(t *testing.T) {
	t.Parallel()

	if _, err := (Credentials{Project: Named("acme-prod")}).Permissions(edge.PurposeDeploy, ""); err == nil {
		t.Errorf("a deploy credential for no tier rendered its roles, want a refusal: every deploy credential is fenced to one tier")
	}
}
