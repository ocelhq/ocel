package bootstrap

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/api/cloudresourcemanager/v1"
	firestoreadmin "google.golang.org/api/firestore/v1"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/edges/alb"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
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
	"roles/secretmanager.admin",
	"roles/artifactregistry.admin",
	"roles/iam.serviceAccountAdmin",
	"roles/iam.roleAdmin",
	"roles/resourcemanager.projectIamAdmin",
	"roles/cloudscheduler.admin",
	"roles/compute.networkAdmin",
	"roles/cloudsql.viewer",
	"roles/cloudtasks.admin",
}

var deployRoles = []string{
	"roles/datastore.user",
	"roles/storage.objectAdmin",
	"roles/cloudkms.cryptoKeyEncrypterDecrypter",
	"roles/secretmanager.secretAccessor",
	"roles/artifactregistry.writer",
	"roles/iam.serviceAccountUser",
	"roles/run.admin",
	"roles/memorystore.admin",
	"roles/pubsub.admin",
	"roles/cloudscheduler.admin",
	"roles/logging.viewer",
}

const (
	realtimeSecretsRole = "roles/secretmanager.editor"
	gateSecretsRole     = "roles/secretmanager.admin"
	ProjectNumberHole   = "PROJECT_NUMBER"
)

func prefixedSecretsGrant(role, prefix string) string {
	return fmt.Sprintf("%s, on the condition resource.name.startsWith(%q)",
		role, "projects/"+ProjectNumberHole+"/secrets/"+prefix)
}

func GateSecretsGrant(names names.Names) string {
	return prefixedSecretsGrant(gateSecretsRole, names.GateSecretPrefix())
}

func RealtimeSecretsGrant(names names.Names) string {
	return prefixedSecretsGrant(realtimeSecretsRole, names.RealtimeSecretPrefix())
}

func QueueGrants(names names.Names, region string) []string {
	var granted []string
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		granted = append(granted, grants.QueueAdminGrant(names.DelayQueuePath(region, tier)))
	}
	return granted
}

const untaggingRole = "roles/artifactregistry.repoAdmin"

func UntaggingGrants(names names.Names, region string) []string {
	var grants []string
	for _, tier := range []environment.Tier{environment.TierPreview, environment.TierProduction} {
		grants = append(grants, fmt.Sprintf("%s, on the repository projects/%s/locations/%s/repositories/%s",
			untaggingRole, names.Project(), region, names.Repository(tier)))
	}
	return grants
}

const (
	bucketsRole   = "roles/storage.admin"
	databasesRole = "roles/cloudsql.admin"
)

var projectPermissions = []string{
	"storage.buckets.create",
	"cloudsql.instances.create",
	"cloudsql.instances.list",
	"cloudsql.instances.get",
	"secretmanager.secrets.create",
}

func AppBucketsGrant(names names.Names) string {
	return fmt.Sprintf("%s, on the condition resource.name.startsWith(%q)",
		bucketsRole, "projects/_/buckets/"+names.AppBucketPrefix())
}

func DatabasesGrant(names names.Names) string {
	return fmt.Sprintf("%s, on the condition resource.name.startsWith(%q)",
		databasesRole, "projects/"+names.Project()+"/instances/"+names.PostgresInstancePrefix())
}

func PartsGrant(names names.Names) string {
	return names.PartsRolePath() + ", unconditioned: Compute Engine, Network Connectivity, Artifact Registry and Service Usage name no resource attribute an IAM condition could hold a part by"
}

func ProjectPermissionsGrant() string {
	return "a custom role holding " + strings.Join(projectPermissions, ", ") +
		", unconditioned: Google checks creating a bucket, an instance or a secret, listing instances and reading Cloud SQL operations against the project, " +
		"where no name condition admits them"
}

func RolesFor(purpose edge.CredentialPurpose) []string {
	if purpose != edge.PurposeBootstrap {
		return deployRoles
	}
	granted := slices.Clone(deployRoles)
	for _, role := range bootstrapRoles {
		if !slices.Contains(granted, role) {
			granted = append(granted, role)
		}
	}
	return granted
}

func (b Engine) preflight(ctx context.Context, read Survey, parts []string) error {
	if err := b.servicesOn(ctx, APIsFor(read.Tier, parts)); err != nil {
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
	roles := RolesFor(edge.PurposeBootstrap)
	if slices.Contains(parts, CloudflareOriginPart) {
		roles = AppendMissing(roles, alb.ShieldedRoles())
	}
	return roles
}

func (b Engine) servicesOn(ctx context.Context, apis []string) error {
	service, err := b.Clients.Services()
	if err != nil {
		return err
	}
	var off []string
	for _, api := range apis {
		name := "projects/" + b.Clients.Project() + "/services/" + api
		apiService, err := googleretry.Attempted(ctx, service.Services.Get(name).Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("read whether %s is on in project %s: %w", api, b.Clients.Project(), err)
		}
		if apiService.State != EnabledService {
			off = append(off, api)
		}
	}
	if len(off) == 0 {
		return nil
	}
	return refusal.Refuse(refusal.CodeNotReady,
		"project %s has %s switched off, and ocel provisions resources rather than switching on the services that host them.\n"+
			"Run `gcloud services enable %s --project %s`, then try again",
		b.Clients.Project(), strings.Join(off, ", "), strings.Join(off, " "), b.Clients.Project())
}

func (b Engine) Permitted(ctx context.Context, parts []string) error {
	service, err := b.Clients.Projects()
	if err != nil {
		return err
	}
	needed := PermissionsFor(parts)
	var granted []string
	for asked := range slices.Chunk(needed, testedPermissionsPerRequest) {
		answer, err := googleretry.Attempted(ctx, service.Projects.TestIamPermissions(b.Clients.Project(),
			&cloudresourcemanager.TestIamPermissionsRequest{Permissions: asked}).Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("ask project %s what this credential may do in it: %w", b.Clients.Project(), err)
		}
		granted = append(granted, answer.Permissions...)
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
