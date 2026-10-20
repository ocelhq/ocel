package gcp

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
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
		if !slices.Contains(bootstrap.RolesFor(edge.PurposeBootstrap), role) {
			t.Errorf("a bootstrap credential is granted %v, want %s", bootstrap.RolesFor(edge.PurposeBootstrap), role)
		}
	}
	if !slices.Contains(bootstrap.BootstrapAPIs, "cloudresourcemanager.googleapis.com") {
		t.Errorf("a bootstrap checks %v are on, want the Resource Manager API every tag call goes to", bootstrap.BootstrapAPIs)
	}
}

func TestADeployCredentialMayBindBothTierValuesAndNoOtherTag(t *testing.T) {
	t.Parallel()

	if slices.Contains(bootstrap.RolesFor(edge.PurposeDeploy), "roles/resourcemanager.tagUser") {
		t.Errorf("a deploy credential is granted roles/resourcemanager.tagUser on the whole project, and with it every tag value in it")
	}
	document, err := (Credentials{Project: Named("acme-prod")}).Permissions(edge.PurposeDeploy, "")
	if err != nil {
		t.Fatal(err)
	}
	want := "roles/resourcemanager.tagUser, on the tag values acme-prod/ocel-tier/production and acme-prod/ocel-tier/preview"
	if !slices.Contains(strings.Split(document.Document, "\n"), want) {
		t.Errorf("a deploy credential is granted\n%s\nwant the line %q", document.Document, want)
	}
}

func TestADeployMayBindTheKindsItMakesWhereNoRoleItHoldsAlreadyLetsIt(t *testing.T) {
	t.Parallel()

	names := names.New("ocel", "acme-prod")
	for _, permission := range []string{"iam.serviceAccounts.createTagBinding", "iam.serviceAccounts.listTagBindings"} {
		if !slices.Contains(bootstrap.AppAccountsPermissions, permission) {
			t.Errorf("the app accounts role holds %v, want %s: a deploy tags the account each app runs as", bootstrap.AppAccountsPermissions, permission)
		}
	}
	for _, permission := range []string{"datastore.databases.createTagBinding", "datastore.databases.listTagBindings", "datastore.databases.deleteTagBinding"} {
		if !slices.Contains(bootstrap.NewProjectRecordsRole(names).Permissions, permission) {
			t.Errorf("the project records role holds %v, want %s: a deploy tags the databases it makes and untags one before deleting it", bootstrap.NewProjectRecordsRole(names).Permissions, permission)
		}
	}
	if parts := bootstrap.NewPartsRole(names).Permissions; !slices.Contains(parts, "artifactregistry.repositories.createTagBinding") {
		t.Errorf("the parts role holds %v, want artifactregistry.repositories.createTagBinding: a deploy tags the alb gate plugin repository it makes", parts)
	}
	if grant := bootstrap.ProjectPermissionsGrant(); !strings.Contains(grant, "secretmanager.secrets.createTagBinding") || !strings.Contains(grant, "secretmanager.secrets.deleteTagBinding") {
		t.Errorf("a deploy is granted %q, want the secret tag bindings its realtime secrets' role lacks", grant)
	}
}
