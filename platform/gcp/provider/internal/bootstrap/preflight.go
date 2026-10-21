package bootstrap

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/api/cloudresourcemanager/v1"
	firestoreadmin "google.golang.org/api/firestore/v1"

	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
)

const (
	EnabledService              = "ENABLED"
	testedPermissionsPerRequest = 100
)

var BootstrapPermissions = []string{
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
	"storage.buckets.create",
	"storage.buckets.update",
	"storage.buckets.delete",
	"cloudkms.keyRings.create",
	"cloudkms.keyRings.get",
	"cloudkms.cryptoKeys.create",
	"cloudkms.cryptoKeys.get",
	"cloudkms.cryptoKeyVersions.create",
	"cloudkms.cryptoKeyVersions.get",
	"cloudkms.cryptoKeyVersions.destroy",
	"cloudkms.cryptoKeys.getIamPolicy",
	"cloudkms.cryptoKeys.setIamPolicy",
	"secretmanager.secrets.create",
	"secretmanager.secrets.get",
	"secretmanager.secrets.delete",
	"secretmanager.versions.add",
	"secretmanager.versions.get",
	"secretmanager.versions.access",
	"artifactregistry.repositories.create",
	"artifactregistry.repositories.get",
	"artifactregistry.repositories.update",
	"artifactregistry.repositories.delete",
	"iam.serviceAccounts.create",
	"iam.serviceAccounts.get",
	"iam.serviceAccounts.list",
	"iam.serviceAccounts.delete",
	"iam.serviceAccounts.getIamPolicy",
	"iam.serviceAccounts.setIamPolicy",
	"iam.roles.get",
	"iam.roles.create",
	"iam.roles.update",
	"iam.roles.undelete",
	"iam.serviceAccounts.actAs",
	"resourcemanager.projects.getIamPolicy",
	"resourcemanager.projects.setIamPolicy",
	"artifactregistry.repositories.uploadArtifacts",
	"artifactregistry.repositories.downloadArtifacts",
	"run.services.create",
	"run.services.get",
	"run.services.update",
	"run.services.delete",
	"run.services.getIamPolicy",
	"run.services.setIamPolicy",
	"run.operations.get",
	gcloud.RunLocationsPermission,
	"cloudscheduler.jobs.create",
	"cloudscheduler.jobs.get",
	"cloudscheduler.jobs.update",
	"cloudscheduler.jobs.delete",
	"cloudscheduler.jobs.enable",
	"cloudtasks.queues.create",
	"cloudtasks.queues.get",
	"cloudtasks.queues.purge",
	"cloudtasks.queues.getIamPolicy",
	"cloudtasks.queues.setIamPolicy",
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
	"logging.views.create",
	"logging.views.get",
	"logging.views.update",
	"logging.views.delete",
}

var AssetStorePermissions = []string{
	"storage.hmacKeys.create",
	"storage.hmacKeys.list",
	"storage.hmacKeys.get",
	"storage.hmacKeys.update",
	"storage.hmacKeys.delete",
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
	gcloud.TierTagUserRole,
}

func BootstrapRoles() []string { return slices.Clone(bootstrapRoles) }

func (b Engine) preflight(ctx context.Context, read Survey, parts []string) error {
	if err := b.RefuseServicesOff(ctx, APIsFor(read.Tier, parts)); err != nil {
		return err
	}
	if err := b.Permitted(ctx, parts); err != nil {
		return err
	}
	return b.RegionServed(ctx, read)
}

func PermissionsFor(parts []string) []string {
	permissions := slices.Clone(BootstrapPermissions)
	if slices.Contains(parts, CloudflareOriginPart) {
		permissions = AppendMissing(permissions, alb.ShieldedPermissions)
		permissions = AppendMissing(permissions, AssetStorePermissions)
	}
	return permissions
}

func RolesCovering(parts []string) []string {
	roles := BootstrapRoles()
	if slices.Contains(parts, CloudflareOriginPart) {
		roles = AppendMissing(roles, alb.ShieldedRoles())
	}
	return roles
}

func (b Engine) listServicesOff(ctx context.Context, apis []string) ([]string, error) {
	if len(apis) == 0 {
		return nil, nil
	}
	service, err := b.Clients.Services()
	if err != nil {
		return nil, err
	}
	var off []string
	for _, api := range apis {
		name := "projects/" + b.Clients.Project() + "/services/" + api
		apiService, err := googleretry.Attempted(ctx, service.Services.Get(name).Context(ctx).Do)
		if err != nil {
			return nil, fmt.Errorf("read whether %s is on in project %s: %w", api, b.Clients.Project(), err)
		}
		if apiService.State != EnabledService {
			off = append(off, api)
		}
	}
	return off, nil
}

func (b Engine) RefuseServicesOff(ctx context.Context, apis []string) error {
	off, err := b.listServicesOff(ctx, apis)
	if err != nil || len(off) == 0 {
		return err
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"project %s has %s switched off, and ocel provisions resources rather than switching on the services that host them.\n"+
			"Run `gcloud services enable %s --project %s`, then try again",
		b.Clients.Project(), strings.Join(off, ", "), strings.Join(off, " "), b.Clients.Project())
}

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
	needed := PermissionsFor(parts)
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
	return refusal.Refuse(refusal.CodeDenied,
		"this credential may not do what a bootstrap of project %s does: it lacks %s.\n"+
			"Granting %s covers every one of them, but the permissions are what is checked",
		b.Clients.Project(), strings.Join(missing, ", "), strings.Join(RolesCovering(parts), ", "))
}

func (b Engine) RegionServed(ctx context.Context, read Survey) error {
	if read.Emulated {
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
