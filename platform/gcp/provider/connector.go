package gcp

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"
	"time"

	"google.golang.org/api/googleapi"
	"google.golang.org/api/iam/v1"
	runv2 "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/connectorserver"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/pkg/target"
	"github.com/ocelhq/ocel/platform/gcp/provider/googleretry"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/policy"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/run"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	ConnectorOS   = "linux"
	ConnectorArch = "amd64"

	connectorImageName   = "ocel-connector"
	connectorImagePath   = "/connector"
	connectorMemory      = 256
	connectorInstances   = 1
	connectorTimeout     = 30 * time.Second
	connectorVersionEnv  = "OCEL_CONNECTOR_VERSION"
	connectorRecordsRole = "roles/datastore.user"
	connectorSealingRole = "roles/cloudkms.cryptoKeyEncrypter"
	connectorOpeningRole = "roles/cloudkms.cryptoKeyDecrypter"
	connectorAccountNote = "the identity the ocel connector answers the console as"
)

const connectorCompute = provider.ComputeServerless

type connector struct{ *Provider }

func (p connector) Target(ctx context.Context) (provider.ConnectorTarget, error) {
	names, err := p.Names(ctx)
	if err != nil {
		return provider.ConnectorTarget{}, err
	}
	if err := names.ConnectorFits(); err != nil {
		return provider.ConnectorTarget{}, err
	}
	fingerprint, err := target.Fingerprint("gcp", names.Project(), p.options.Region, string(names.Namespace()))
	if err != nil {
		return provider.ConnectorTarget{}, err
	}
	described := provider.ConnectorTarget{Fingerprint: fingerprint, OS: ConnectorOS, Arch: ConnectorArch}

	deployed, err := p.connectorService(ctx)
	if err != nil {
		return provider.ConnectorTarget{}, err
	}
	if deployed == nil {
		return described, nil
	}
	described.Hostname = run.HostOf(deployed.Uri)
	described.Installed = &provider.InstalledConnector{
		Version:   connectorVersionOf(deployed),
		PublicKey: connectorPublicKeyOf(deployed),
		Compute:   connectorCompute,
	}
	return described, nil
}

func (p connector) Install(ctx context.Context, install provider.ConnectorInstall,
	progress progress.Log) (provider.ConnectorAddress, error) {
	compute, err := provider.ConnectorCompute(install.Compute, connectorCompute)
	if err != nil {
		return provider.ConnectorAddress{}, err
	}
	names, err := p.Names(ctx)
	if err != nil {
		return provider.ConnectorAddress{}, err
	}
	if err := names.ConnectorFits(); err != nil {
		return provider.ConnectorAddress{}, err
	}
	if len(install.Config) == 0 {
		return provider.ConnectorAddress{}, refusal.Refuse(refusal.CodeInvalid,
			"this install includes no connector config, so nothing would name the console the service trusts")
	}
	if p.emulated() {
		return provider.ConnectorAddress{}, refusal.Refuse(refusal.CodeNotReady,
			"this run talks to an emulator, which deploys no Cloud Run service and hands out no url a console could dial: add the connector against the project itself")
	}

	trust, err := connectorserver.ParseConfig(install.Config, "this install")
	if err != nil {
		return provider.ConnectorAddress{}, refusal.Refuse(refusal.CodeInvalid, "%s", err)
	}

	image, err := p.pushedConnector(ctx, install.Binary, progress)
	if err != nil {
		return provider.ConnectorAddress{}, err
	}
	if err := p.ensureConnectorAccount(ctx, trust.Grants, progress); err != nil {
		return provider.ConnectorAddress{}, err
	}
	publicKey, err := p.connectorKey(ctx, progress)
	if err != nil {
		return provider.ConnectorAddress{}, err
	}
	if err := p.bindConnectorKeySecret(ctx, true); err != nil {
		return provider.ConnectorAddress{}, err
	}
	config, err := keyPathed(install.Config, connectorKeyPath)
	if err != nil {
		return provider.ConnectorAddress{}, err
	}

	released, err := p.services().DeployAndRoute(ctx,
		buildConnectorServing(names, p.options.Region, image, install.Version, publicKey, config), progress)
	if err != nil {
		return provider.ConnectorAddress{}, err
	}
	if released.Url == "" {
		return provider.ConnectorAddress{}, refusal.Refuse(refusal.CodeNotReady,
			"%s is deployed and published no url, so the console has nothing to dial", names.Connector())
	}
	clients, err := p.openClients(ctx)
	if err != nil {
		return provider.ConnectorAddress{}, err
	}
	if err := scheduleConnectorHeartbeat(ctx, clients, released.Url); err != nil {
		return provider.ConnectorAddress{}, err
	}
	gcloud.EnsureProgress(progress).Say("Scheduled Cloud Scheduler job " + names.Connector() + " to wake the connector for a heartbeat every minute")
	return provider.ConnectorAddress{URL: released.Url, PublicKey: publicKey, Compute: compute}, nil
}

func buildConnectorServing(names names.Names, region, image, version, publicKey string, config []byte) run.Serving {
	return run.Serving{
		Service:   names.Connector(),
		Image:     image,
		Account:   names.ConnectorAccountEmail(),
		Compute:   provider.ComputeServerless,
		Public:    true,
		Memory:    connectorMemory,
		Timeout:   connectorTimeout,
		Ingress:   run.IngressEverywhere,
		Instances: provider.Instances{Max: connectorInstances},
		Mounts:    []run.SecretMount{connectorKeyMount(names.ConnectorKeySecret())},
		Env: map[string]string{
			provider.NamespaceEnvVar:       string(names.Namespace()),
			ports.ProjectEnvVar:            names.Project(),
			ports.RegionEnvVar:             region,
			ports.ConnectorAccountEnvVar:   names.ConnectorAccountEmail(),
			connectorVersionEnv:            version,
			connectorPublicKeyEnv:          publicKey,
			provider.ConnectorConfigEnvVar: string(config),
		},
	}
}

func (p connector) Remove(ctx context.Context, progress progress.Log) error {
	clients, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	return everyStep(
		func() error { return takeConnectorSchedule(ctx, clients, progress) },
		func() error { return p.services().TearDown(ctx, clients.Connector(), progress) },
		func() error { return p.forgetConnectorGrants(ctx, progress) },
		func() error { return p.takeConnectorKey(ctx, progress) },
		func() error { return p.takeConnectorAccount(ctx, progress) },
		func() error { return p.takeConnectorImages(ctx, progress) },
	)
}

func everyStep(steps ...func() error) error {
	errs := make([]error, 0, len(steps))
	for _, step := range steps {
		errs = append(errs, step())
	}
	return errors.Join(errs...)
}

func (p *Provider) connectorService(ctx context.Context) (*runv2.GoogleCloudRunV2Service, error) {
	clients, err := p.openClients(ctx)
	if err != nil {
		return nil, err
	}
	services, err := clients.Run()
	if err != nil {
		return nil, err
	}
	path := clients.ServicePath(clients.Connector())
	found, err := googleretry.Attempted(ctx, func(call ...googleapi.CallOption) (*runv2.GoogleCloudRunV2Service, error) {
		return services.Projects.Locations.Services.Get(path).Context(ctx).Do(call...)
	})
	if gcloud.Absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the Cloud Run service %s: %w", clients.Connector(), err)
	}
	return found, nil
}

func connectorVersionOf(service *runv2.GoogleCloudRunV2Service) string {
	if service.Template == nil {
		return ""
	}
	for _, container := range service.Template.Containers {
		for _, entry := range container.Env {
			if entry.Name == connectorVersionEnv {
				return entry.Value
			}
		}
	}
	return ""
}

func (p *Provider) pushedConnector(ctx context.Context, binary []byte, progress progress.Log) (string, error) {
	if len(binary) == 0 {
		return "", refusal.Refuse(refusal.CodeInvalid,
			"this install includes no connector binary to put in an image")
	}
	base, err := p.based(ctx, run.StaticImage)
	if err != nil {
		return "", err
	}
	built, err := binaryImage(base, binary, connectorImagePath)
	if err != nil {
		return "", err
	}
	digest, err := built.Digest()
	if err != nil {
		return "", fmt.Errorf("read the digest of the connector image: %w", err)
	}
	names, err := p.Names(ctx)
	if err != nil {
		return "", err
	}
	ref := names.RepositoryPath(p.options.Region, environment.TierProduction) +
		"/" + connectorImageName + ":" + naming.DigestTag(digest.String())
	if err := p.pushImage(ctx, environment.TierProduction, connectorImageName, ref, built, progress); err != nil {
		return "", err
	}
	return ref, nil
}

func (p *Provider) ensureConnectorAccount(ctx context.Context, grants []string, progress progress.Log) error {
	names, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	service, err := names.Accounts()
	if err != nil {
		return err
	}
	_, err = googleretry.Attempted(ctx, service.Projects.ServiceAccounts.Create("projects/"+names.Project(),
		&iam.CreateServiceAccountRequest{
			AccountId: names.Connector(),
			ServiceAccount: &iam.ServiceAccount{
				DisplayName: "ocel connector",
				Description: connectorAccountNote,
			},
		}).Context(ctx).Do)
	switch {
	case err == nil:
		gcloud.EnsureProgress(progress).Say("Created service account " + names.ConnectorAccountEmail() + " for the connector to run as")
	case gcloud.Taken(err):
		gcloud.EnsureProgress(progress).Debug("The connector runs as service account " + names.ConnectorAccountEmail() + ", which already exists")
	default:
		return fmt.Errorf("create the %s service account: %w", names.Connector(), err)
	}
	if err := p.bindConnectorProject(ctx, true); err != nil {
		return err
	}
	return p.bindConnectorKeys(ctx, keyRolesFor(grants), progress)
}

func keyRolesFor(grants []string) []string {
	var roles []string
	if slices.Contains(grants, connectorserver.CapabilityVariablesWrite) {
		roles = append(roles, connectorSealingRole)
	}
	if slices.Contains(grants, connectorserver.CapabilityVariablesReveal) {
		roles = append(roles, connectorOpeningRole)
	}
	return roles
}

func (p *Provider) forgetConnectorGrants(ctx context.Context, progress progress.Log) error {
	return everyStep(
		func() error { return p.bindConnectorProject(ctx, false) },
		func() error { return p.bindConnectorKeys(ctx, nil, progress) },
	)
}

func (p *Provider) bindConnectorProject(ctx context.Context, granting bool) error {
	clients, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	member := "serviceAccount:" + clients.ConnectorAccountEmail()
	if err := gcloud.BindProjectRole(ctx, clients, member, connectorRecordsRole, connectorRecordsCondition(clients), granting); err != nil {
		return fmt.Errorf("let %s read and write the %s database's records and every project's: %w", member, ports.Database(clients.Namespace()), err)
	}
	return nil
}

func connectorRecordsCondition(c *gcloud.Clients) *policy.Condition {
	clauses := []string{fmt.Sprintf("resource.name == %q", bootstrap.DatabasePath(c, c.Database()))}
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		clauses = append(clauses, fmt.Sprintf("resource.name.startsWith(%q)", bootstrap.DatabasePath(c, c.ProjectDatabasePrefix(tier))))
	}
	return &policy.Condition{Title: "ocel " + string(c.Namespace()) + " records", Expression: strings.Join(clauses, " || ")}
}

func (p *Provider) bindConnectorKeys(ctx context.Context, wanted []string, progress progress.Log) error {
	clients, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	member := "serviceAccount:" + clients.ConnectorAccountEmail()
	var errs []error
	for _, tier := range []environment.Tier{environment.TierProduction, environment.TierPreview} {
		changed, err := gcloud.BindKeyRoles(ctx, clients, tier, member, connectorKeyRoles, wanted)
		if err != nil && len(wanted) > 0 {
			return err
		}
		projectChanged, projectErr := bindProjectKeyRoles(ctx, clients, tier, member, connectorKeyRoles, wanted)
		if projectErr != nil && len(wanted) > 0 {
			return projectErr
		}
		changed = changed || projectChanged
		err = errors.Join(err, projectErr)
		if changed && len(wanted) > 0 {
			gcloud.EnsureProgress(progress).Say("Granted the connector " + strings.Join(wanted, " and ") + " on the " + string(tier) + " KMS key")
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

var connectorKeyRoles = []string{connectorSealingRole, connectorOpeningRole}

func (p *Provider) takeConnectorAccount(ctx context.Context, progress progress.Log) error {
	clients, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	service, err := clients.Accounts()
	if err != nil {
		return err
	}
	name := clients.Connector()
	if _, err := googleretry.Attempted(ctx, service.Projects.ServiceAccounts.Delete(
		gcloud.AccountPath(clients, name)).Context(ctx).Do); err != nil && !gcloud.Absent(err) {
		return fmt.Errorf("delete the %s service account: %w", name, err)
	}
	gcloud.EnsureProgress(progress).Say("Deleted service account " + clients.ConnectorAccountEmail() + ", which the connector ran as")
	return nil
}

func (p *Provider) takeConnectorImages(ctx context.Context, progress progress.Log) error {
	clients, err := p.openClients(ctx)
	if err != nil {
		return err
	}
	service, err := clients.Repositories()
	if err != nil {
		return err
	}
	packagePath := gcloud.RepositoryPath(clients, clients.Repository(environment.TierProduction)) +
		"/packages/" + connectorImageName
	if _, err := googleretry.Attempted(ctx, service.Projects.Locations.Repositories.Packages.Delete(
		packagePath).Context(ctx).Do); err != nil && !gcloud.Absent(err) {
		return fmt.Errorf("delete the connector images at %s: %w", packagePath, err)
	}
	gcloud.EnsureProgress(progress).Say("Deleted the connector's images from repository " + clients.Repository(environment.TierProduction))
	return nil
}
