package gcp

import (
	"slices"
	"strings"
	"testing"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
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

func TestADeployCredentialMayBindItsOwnTiersValueAndNoOtherTag(t *testing.T) {
	t.Parallel()

	if slices.Contains(bootstrap.RolesFor(edge.PurposeDeploy), "roles/resourcemanager.tagUser") {
		t.Errorf("a deploy credential is granted roles/resourcemanager.tagUser on the whole project, and with it every tag value in it")
	}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		document, err := (Credentials{Project: Named("acme-prod")}).Permissions(edge.PurposeDeploy, tier)
		if err != nil {
			t.Fatal(err)
		}
		lines := strings.Split(document.Document, "\n")
		if want := "roles/resourcemanager.tagUser, on the tag value acme-prod/ocel-tier/" + string(tier); !slices.Contains(lines, want) {
			t.Errorf("a %s deploy credential is granted\n%s\nwant the line %q", tier, document.Document, want)
		}
		if other := "acme-prod/ocel-tier/" + string(tier.Sibling()); strings.Contains(document.Document, other) {
			t.Errorf("a %s deploy credential is granted\n%s\nwhich reaches %s, and with it the other tier's resources", tier, document.Document, other)
		}
		if !strings.Contains(document.Heading, string(tier)) {
			t.Errorf("the %s deploy credential's roles are printed under %q, which does not say which tier they are for", tier, document.Heading)
		}
	}
}

func TestADeployCredentialForNoTierIsRefused(t *testing.T) {
	t.Parallel()

	if _, err := (Credentials{Project: Named("acme-prod")}).Permissions(edge.PurposeDeploy, ""); err == nil {
		t.Errorf("a deploy credential for no tier rendered its roles, want a refusal: every deploy credential is fenced to one tier")
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
