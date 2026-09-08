package gcp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"google.golang.org/api/cloudresourcemanager/v1"
	firestoreadmin "google.golang.org/api/firestore/v1"

	"github.com/ocelhq/ocel/pkg/providerkit"
)

const enabledService = "ENABLED"

var bootstrapPermissions = []string{
	"datastore.databases.create",
	"datastore.databases.get",
	"datastore.databases.update",
	"datastore.databases.delete",
	"storage.buckets.create",
	"storage.buckets.get",
	"storage.buckets.delete",
	"storage.objects.create",
	"storage.objects.list",
	"storage.objects.delete",
	"cloudkms.keyRings.create",
	"cloudkms.keyRings.get",
	"cloudkms.cryptoKeys.create",
	"cloudkms.cryptoKeys.get",
	"cloudkms.cryptoKeyVersions.create",
	"cloudkms.cryptoKeyVersions.get",
	"cloudkms.cryptoKeyVersions.destroy",
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
	"iam.serviceAccounts.delete",
	"iam.serviceAccounts.getIamPolicy",
	"iam.serviceAccounts.setIamPolicy",
}

var bootstrapRoles = []string{
	"roles/datastore.owner",
	"roles/storage.admin",
	"roles/cloudkms.admin",
	"roles/secretmanager.admin",
	"roles/artifactregistry.admin",
	"roles/iam.serviceAccountAdmin",
}

var deployRoles = []string{
	"roles/datastore.user",
	"roles/storage.admin",
	"roles/cloudkms.cryptoKeyEncrypterDecrypter",
	"roles/secretmanager.secretAccessor",
	"roles/artifactregistry.writer",
	"roles/iam.serviceAccountUser",
	"roles/run.developer",
	"roles/run.admin",
}

func rolesFor(tier providerkit.CredentialTier) []string {
	if tier != providerkit.TierBootstrap {
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

func (b bootstrapper) preflight(ctx context.Context, read survey) error {
	if err := b.servicesOn(ctx); err != nil {
		return err
	}
	if err := b.permitted(ctx); err != nil {
		return err
	}
	return b.regionServed(ctx, read)
}

func (b bootstrapper) servicesOn(ctx context.Context) error {
	service, err := b.clients.Services()
	if err != nil {
		return err
	}
	var off []string
	for _, api := range BootstrapAPIs {
		name := "projects/" + b.clients.project + "/services/" + api
		held, err := attempted(ctx, service.Services.Get(name).Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("read whether %s is on in project %s: %w", api, b.clients.project, err)
		}
		if held.State != enabledService {
			off = append(off, api)
		}
	}
	if len(off) == 0 {
		return nil
	}
	return providerkit.Refuse(providerkit.CodeNotReady,
		"project %s has %s switched off, and ocel stands up resources rather than switching on the services that hold them.\n"+
			"Run `gcloud services enable %s --project %s`, then try again",
		b.clients.project, strings.Join(off, ", "), strings.Join(off, " "), b.clients.project)
}

func (b bootstrapper) permitted(ctx context.Context) error {
	service, err := b.clients.Projects()
	if err != nil {
		return err
	}
	granted, err := attempted(ctx, service.Projects.TestIamPermissions(b.clients.project,
		&cloudresourcemanager.TestIamPermissionsRequest{Permissions: bootstrapPermissions}).Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("ask project %s what this credential may do in it: %w", b.clients.project, err)
	}
	var missing []string
	for _, permission := range bootstrapPermissions {
		if !slices.Contains(granted.Permissions, permission) {
			missing = append(missing, permission)
		}
	}
	if len(missing) == 0 {
		return nil
	}
	return providerkit.Refuse(providerkit.CodeDenied,
		"this credential may not do what a bootstrap of project %s does: it lacks %s.\n"+
			"Granting %s covers every one of them, but the permissions are what is checked",
		b.clients.project, strings.Join(missing, ", "), strings.Join(bootstrapRoles, ", "))
}

func (b bootstrapper) regionServed(ctx context.Context, read survey) error {
	if read.Emulated {
		return nil
	}
	service, err := b.clients.Databases()
	if err != nil {
		return err
	}
	held, err := attempted(ctx, service.Projects.Locations.List("projects/"+b.clients.project).Context(ctx).Do)
	if err != nil {
		return fmt.Errorf("ask which locations Firestore serves project %s from: %w", b.clients.project, err)
	}
	served := make([]string, 0, len(held.Locations))
	for _, location := range held.Locations {
		served = append(served, locationID(location))
	}
	if slices.Contains(served, read.Region) {
		return nil
	}
	slices.Sort(served)
	return providerkit.Refuse(providerkit.CodeInvalid,
		"option %q names %s, and the one region a bootstrap is given holds this project's database as well as its buckets and keys: "+
			"Firestore does not serve %s.\nName one Firestore serves: %s",
		"region", read.Region, read.Region, strings.Join(served, ", "))
}

func locationID(location *firestoreadmin.Location) string {
	if location.LocationId != "" {
		return location.LocationId
	}
	return location.Name
}
