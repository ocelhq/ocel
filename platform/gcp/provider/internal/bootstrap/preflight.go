package bootstrap

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/api/cloudresourcemanager/v1"
	firestoreadmin "google.golang.org/api/firestore/v1"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const testedPermissionsPerRequest = 100

var BootstrapGrants = gcloud.Grants{
	{Role: "roles/datastore.owner", Permissions: []string{
		"datastore.databases.create",
		"datastore.databases.get",
		"datastore.databases.update",
		"datastore.databases.delete",
		"datastore.databases.getMetadata",
		"datastore.operations.get",
		"datastore.locations.list",
		"datastore.schemas.create",
		"datastore.schemas.list",
		"datastore.schemas.update",
		"datastore.databases.listTagBindings",
		"datastore.databases.deleteTagBinding",
	}},
	{Role: "roles/storage.admin", Permissions: []string{
		"storage.buckets.create",
		"storage.buckets.delete",
		"storage.buckets.createTagBinding",
		"storage.buckets.listTagBindings",
	}},
	{Role: "roles/cloudkms.admin", Permissions: []string{
		"cloudkms.keyRings.create",
		"cloudkms.keyRings.get",
		"cloudkms.cryptoKeys.create",
		"cloudkms.cryptoKeys.get",
		"cloudkms.cryptoKeyVersions.create",
		"cloudkms.cryptoKeyVersions.get",
		"cloudkms.cryptoKeyVersions.destroy",
		"cloudkms.cryptoKeys.getIamPolicy",
		"cloudkms.cryptoKeys.setIamPolicy",
	}},
	{Role: "roles/secretmanager.admin", Permissions: []string{
		"secretmanager.secrets.create",
		"secretmanager.secrets.get",
		"secretmanager.secrets.delete",
		"secretmanager.versions.add",
		"secretmanager.versions.get",
		"secretmanager.versions.access",
		"secretmanager.secrets.createTagBinding",
		"secretmanager.secrets.listTagBindings",
		"secretmanager.secrets.deleteTagBinding",
	}},
	{Role: "roles/artifactregistry.admin", Permissions: []string{
		"artifactregistry.repositories.create",
		"artifactregistry.repositories.get",
		"artifactregistry.repositories.update",
		"artifactregistry.repositories.delete",
		"artifactregistry.repositories.uploadArtifacts",
		"artifactregistry.repositories.downloadArtifacts",
		"artifactregistry.repositories.createTagBinding",
		"artifactregistry.repositories.listTagBindings",
	}},
	{Role: "roles/iam.serviceAccountAdmin", Permissions: []string{
		"iam.serviceAccounts.create",
		"iam.serviceAccounts.get",
		"iam.serviceAccounts.list",
		"iam.serviceAccounts.delete",
		"iam.serviceAccounts.getIamPolicy",
		"iam.serviceAccounts.setIamPolicy",
		"iam.serviceAccounts.createTagBinding",
		"iam.serviceAccounts.listTagBindings",
		"iam.serviceAccounts.deleteTagBinding",
	}},
	{Role: "roles/iam.serviceAccountUser", Permissions: []string{
		"iam.serviceAccounts.actAs",
	}},
	{Role: "roles/iam.roleAdmin", Permissions: []string{
		"iam.roles.get",
		"iam.roles.create",
		"iam.roles.update",
		"iam.roles.undelete",
	}},
	{Role: "roles/resourcemanager.projectIamAdmin", Permissions: []string{
		"resourcemanager.projects.getIamPolicy",
		"resourcemanager.projects.setIamPolicy",
	}},
	{Role: "roles/run.admin", Permissions: []string{
		"run.services.create",
		"run.services.get",
		"run.services.update",
		"run.services.delete",
		"run.services.getIamPolicy",
		"run.services.setIamPolicy",
		"run.operations.get",
		gcloud.RunLocationsPermission,
		"run.services.createTagBinding",
		"run.services.listTagBindings",
	}},
	{Role: "roles/cloudscheduler.admin", Permissions: []string{
		"cloudscheduler.jobs.create",
		"cloudscheduler.jobs.get",
		"cloudscheduler.jobs.update",
		"cloudscheduler.jobs.delete",
		"cloudscheduler.jobs.enable",
	}},
	{Role: "roles/cloudtasks.admin", Permissions: []string{
		"cloudtasks.queues.create",
		"cloudtasks.queues.get",
		"cloudtasks.queues.purge",
		"cloudtasks.queues.getIamPolicy",
		"cloudtasks.queues.setIamPolicy",
	}},
	{Role: "roles/resourcemanager.tagAdmin", Permissions: []string{
		"resourcemanager.tagKeys.create",
		"resourcemanager.tagKeys.get",
		"resourcemanager.tagValues.create",
		"resourcemanager.tagValues.get",
	}},
	{Role: gcloud.TierTagUserRole, Permissions: []string{
		"resourcemanager.tagValueBindings.create",
		"resourcemanager.tagValueBindings.delete",
	}},
	{Role: "roles/logging.configWriter", Permissions: []string{
		"logging.views.create",
		"logging.views.get",
		"logging.views.update",
		"logging.views.delete",
	}},
}

var AssetStoreGrants = gcloud.Grants{
	{Role: "roles/storage.hmacKeyAdmin", Permissions: []string{
		"storage.hmacKeys.create",
		"storage.hmacKeys.list",
		"storage.hmacKeys.get",
		"storage.hmacKeys.update",
		"storage.hmacKeys.delete",
	}},
}

var bootstrapRoles = []string{
	"roles/datastore.owner",
	"roles/storage.admin",
	"roles/cloudkms.admin",
	"roles/cloudkms.cryptoKeyEncrypterDecrypter",
	"roles/secretmanager.admin",
	"roles/artifactregistry.admin",
	"roles/iam.serviceAccountAdmin",
	"roles/iam.serviceAccountUser",
	"roles/iam.roleAdmin",
	"roles/resourcemanager.projectIamAdmin",
	"roles/run.admin",
	"roles/memorystore.admin",
	"roles/pubsub.admin",
	"roles/cloudscheduler.admin",
	"roles/compute.networkAdmin",
	"roles/cloudsql.viewer",
	"roles/cloudtasks.admin",
	"roles/logging.viewAccessor",
	"roles/logging.configWriter",
	"roles/resourcemanager.tagAdmin",
	credential.BillingRole,
	gcloud.TierTagUserRole,
}

func BootstrapRoles() []string { return slices.Clone(bootstrapRoles) }

func GrantsFor(parts []string) gcloud.Grants {
	if slices.Contains(parts, CloudflareOriginPart) {
		return slices.Concat(BootstrapGrants, AssetStoreGrants)
	}
	return BootstrapGrants
}

func PermissionsFor(parts []string) []string { return GrantsFor(parts).Permissions() }

func (b Engine) heldPermissions(ctx context.Context, needed []string) ([]string, error) {
	service, err := b.Clients.Projects()
	if err != nil {
		return nil, err
	}
	var granted []string
	for asked := range slices.Chunk(needed, testedPermissionsPerRequest) {
		answer, err := googleretry.Attempted(ctx, service.Projects.TestIamPermissions(b.Clients.Project(),
			&cloudresourcemanager.TestIamPermissionsRequest{Permissions: asked}).Context(ctx).Do)
		if err != nil {
			return nil, fmt.Errorf("ask project %s what this credential may do in it: %w", b.Clients.Project(), err)
		}
		granted = append(granted, answer.Permissions...)
	}
	return granted, nil
}

func (b Engine) Permitted(ctx context.Context, parts []string) error {
	grants := GrantsFor(parts)
	needed := grants.Permissions()
	granted, err := b.heldPermissions(ctx, needed)
	if err != nil {
		return err
	}
	var missing []string
	for _, permission := range needed {
		if !slices.Contains(granted, permission) {
			missing = append(missing, permission)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return refusal.Refuse(refusal.CodeDenied, "this credential lacks %s on project %s; grant it %s",
		strings.Join(missing, ", "), b.Clients.Project(), strings.Join(grants.RolesGranting(missing), ", "))
}

func (b Engine) RegionServed(ctx context.Context, read Survey) error {
	if read.Emulated || b.IsOff(FirestoreAPI) {
		return nil
	}
	service, err := b.Clients.Databases()
	if err != nil {
		return err
	}
	listed, err := googleretry.Attempted(ctx, service.Projects.Locations.List("projects/"+b.Clients.Project()).Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("ask which locations Firestore serves project %s from: %w", b.Clients.Project(), err)
	}
	served := make([]string, 0, len(listed.Locations))
	for _, location := range listed.Locations {
		served = append(served, locationID(location))
	}
	if slices.Contains(served, read.Region) {
		return nil
	}
	slices.Sort(served)
	return refusal.Refuse(refusal.CodeInvalid,
		"option %q names %s, and the one region a bootstrap is given hosts this project's database as well as its buckets and keys: "+
			"Firestore does not serve %s.\nName one Firestore serves: %s",
		"region", read.Region, read.Region, strings.Join(served, ", "))
}

func locationID(location *firestoreadmin.Location) string {
	if location.LocationId != "" {
		return location.LocationId
	}
	return location.Name
}
