package gcp

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net/url"
	"slices"
	"strings"
	"time"

	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iam/v1"
	run "google.golang.org/api/run/v2"

	"github.com/ocelhq/ocel/pkg/connectorkit"
	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/target"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
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

var _ providerkit.ConnectorHost = (*Provider)(nil)

const connectorCompute = providerkit.ComputeServerless

func (p *Provider) DescribeConnectorTarget(ctx context.Context) (providerkit.ConnectorTarget, error) {
	names, err := p.Names(ctx)
	if err != nil {
		return providerkit.ConnectorTarget{}, err
	}
	if err := names.connectorFits(); err != nil {
		return providerkit.ConnectorTarget{}, err
	}
	fingerprint, err := target.Fingerprint("gcp", names.project, p.options.Region, string(names.Namespace()))
	if err != nil {
		return providerkit.ConnectorTarget{}, err
	}
	described := providerkit.ConnectorTarget{Fingerprint: fingerprint, Arch: ConnectorArch}

	standing, err := p.connectorService(ctx)
	if err != nil {
		return providerkit.ConnectorTarget{}, err
	}
	if standing == nil {
		return described, nil
	}
	described.Hostname = hostOf(standing.Uri)
	described.Installed = &providerkit.ConnectorRelease{
		Version:   connectorVersionOf(standing),
		PublicKey: connectorPublicKeyOf(standing),
		Compute:   connectorCompute,
	}
	return described, nil
}

func (p *Provider) InstallConnector(ctx context.Context, install providerkit.ConnectorInstall,
	report providerkit.Reporter) (providerkit.ConnectorAddress, error) {
	compute, err := providerkit.ConnectorCompute(install.Compute, connectorCompute)
	if err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	names, err := p.Names(ctx)
	if err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	if err := names.connectorFits(); err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	if len(install.Config) == 0 {
		return providerkit.ConnectorAddress{}, providerkit.Refuse(providerkit.CodeInvalid,
			"this install carries no connector config, so nothing would name the console the service trusts")
	}
	if p.emulated() {
		return providerkit.ConnectorAddress{}, providerkit.Refuse(providerkit.CodeNotReady,
			"this run talks to an emulator, which stands up no Cloud Run service and hands out no url a console could dial: add the connector against the project itself")
	}

	trust, err := connectorkit.ParseConfig(install.Config, "this install")
	if err != nil {
		return providerkit.ConnectorAddress{}, providerkit.Refuse(providerkit.CodeInvalid, "%s", err)
	}

	image, err := p.pushedConnector(ctx, install.Binary, report)
	if err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	if err := p.standConnectorAccount(ctx, trust.Grants, report); err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	publicKey, err := p.connectorKey(ctx, report)
	if err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	if err := p.bindConnectorKeySecret(ctx, true); err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	config, err := keyPathed(install.Config, connectorKeyPath)
	if err != nil {
		return providerkit.ConnectorAddress{}, err
	}

	released, err := p.stand(ctx, serving{
		service: names.Connector(),
		image:   image,
		account: names.ConnectorAccountEmail(),
		compute: providerkit.ComputeServerless,
		public:  true,
		memory:  connectorMemory,
		timeout: connectorTimeout,
		ingress: ingressEverywhere,
		most:    connectorInstances,
		mounts:  []secretMount{connectorKeyMount(names.ConnectorKeySecret())},
		env: map[string]string{
			providerkit.NamespaceEnvVar:       string(names.Namespace()),
			ports.ProjectEnvVar:               names.project,
			ports.RegionEnvVar:                p.options.Region,
			connectorVersionEnv:               install.Version,
			connectorPublicKeyEnv:             publicKey,
			providerkit.ConnectorConfigEnvVar: string(config),
		},
	}, report)
	if err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	if released.url == "" {
		return providerkit.ConnectorAddress{}, providerkit.Refuse(providerkit.CodeNotReady,
			"%s stands and published no url, so the console has nothing to dial", names.Connector())
	}
	return providerkit.ConnectorAddress{URL: released.url, PublicKey: publicKey, Compute: compute}, nil
}

func (p *Provider) RemoveConnector(ctx context.Context, report providerkit.Reporter) error {
	names, err := p.Names(ctx)
	if err != nil {
		return err
	}
	return everyStep(
		func() error { return p.tearDown(ctx, names.Connector(), report) },
		func() error { return p.forgetConnectorGrants(ctx, report) },
		func() error { return p.takeConnectorKey(ctx, report) },
		func() error { return p.takeConnectorAccount(ctx, report) },
		func() error { return p.takeConnectorImages(ctx, report) },
	)
}

func everyStep(steps ...func() error) error {
	errs := make([]error, 0, len(steps))
	for _, step := range steps {
		errs = append(errs, step())
	}
	return errors.Join(errs...)
}

func (p *Provider) connectorService(ctx context.Context) (*run.GoogleCloudRunV2Service, error) {
	clients, err := p.stood(ctx)
	if err != nil {
		return nil, err
	}
	services, err := clients.Run()
	if err != nil {
		return nil, err
	}
	path := clients.servicePath(clients.Connector())
	held, err := attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleCloudRunV2Service, error) {
		return services.Projects.Locations.Services.Get(path).Context(ctx).Do(call...)
	})
	if absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the Cloud Run service %s: %w", clients.Connector(), err)
	}
	return held, nil
}

func connectorVersionOf(held *run.GoogleCloudRunV2Service) string {
	if held.Template == nil {
		return ""
	}
	for _, container := range held.Template.Containers {
		for _, carried := range container.Env {
			if carried.Name == connectorVersionEnv {
				return carried.Value
			}
		}
	}
	return ""
}

func (p *Provider) pushedConnector(ctx context.Context, binary []byte, report providerkit.Reporter) (string, error) {
	if len(binary) == 0 {
		return "", providerkit.Refuse(providerkit.CodeInvalid,
			"this install carries no connector binary to put in an image")
	}
	base, err := p.based(ctx, staticImage)
	if err != nil {
		return "", err
	}
	built, err := connectorImage(base, binary)
	if err != nil {
		return "", err
	}
	digest, err := built.Digest()
	if err != nil {
		return "", fmt.Errorf("read the digest of the connector image: %w", err)
	}

	at, err := p.ImageRegistry(ctx, providerkit.ClassProduction, nil)
	if err != nil {
		return "", err
	}
	store, err := p.Images(ctx, at)
	if err != nil {
		return "", err
	}
	names, err := p.Names(ctx)
	if err != nil {
		return "", err
	}
	ref := names.RepositoryPath(p.options.Region, providerkit.ClassProduction) +
		"/" + connectorImageName + ":" + naming.DigestTag(digest.String())
	push := providerkit.ImagePush{App: connectorImageName, Target: ref, Digest: digest.String(), Built: built}

	held, err := store.Has(ctx, push)
	if err != nil {
		return "", err
	}
	if held {
		return ref, nil
	}
	if report != nil {
		report.Say("Pushing the connector image to " + ref)
	}
	if err := store.Push(ctx, push, report); err != nil {
		return "", err
	}
	return ref, nil
}

func connectorImage(base v1.Image, binary []byte) (v1.Image, error) {
	var packed bytes.Buffer
	archive := tar.NewWriter(&packed)
	if err := archive.WriteHeader(&tar.Header{
		Typeflag: tar.TypeReg,
		Name:     connectorImagePath[1:],
		Mode:     0o755,
		Size:     int64(len(binary)),
	}); err != nil {
		return nil, fmt.Errorf("open the connector image layer: %w", err)
	}
	if _, err := archive.Write(binary); err != nil {
		return nil, fmt.Errorf("write the connector into its image layer: %w", err)
	}
	if err := archive.Close(); err != nil {
		return nil, fmt.Errorf("close the connector image layer: %w", err)
	}
	layer, err := tarball.LayerFromOpener(func() (io.ReadCloser, error) {
		return io.NopCloser(bytes.NewReader(packed.Bytes())), nil
	})
	if err != nil {
		return nil, err
	}
	appended, err := mutate.Append(base, mutate.Addendum{Layer: layer})
	if err != nil {
		return nil, err
	}
	file, err := appended.ConfigFile()
	if err != nil {
		return nil, err
	}
	config := file.Config
	config.Entrypoint = []string{connectorImagePath}
	config.Cmd = nil
	return mutate.Config(appended, config)
}

func (p *Provider) standConnectorAccount(ctx context.Context, grants []string, report providerkit.Reporter) error {
	names, err := p.stood(ctx)
	if err != nil {
		return err
	}
	service, err := names.Accounts()
	if err != nil {
		return err
	}
	_, err = attempted(ctx, service.Projects.ServiceAccounts.Create("projects/"+names.project,
		&iam.CreateServiceAccountRequest{
			AccountId: names.Connector(),
			ServiceAccount: &iam.ServiceAccount{
				DisplayName: "ocel connector",
				Description: connectorAccountNote,
			},
		}).Context(ctx).Do)
	if err != nil && !taken(err) {
		return fmt.Errorf("create the %s service account: %w", names.Connector(), err)
	}
	if report != nil {
		report.Say("The connector runs as " + names.ConnectorAccountEmail())
	}
	if err := p.bindConnectorProject(ctx, true); err != nil {
		return err
	}
	return p.bindConnectorKeys(ctx, keyRolesFor(grants), report)
}

func keyRolesFor(grants []string) []string {
	var roles []string
	if slices.Contains(grants, connectorkit.CapabilityEnvVarsWrite) {
		roles = append(roles, connectorSealingRole)
	}
	if slices.Contains(grants, connectorkit.CapabilityEnvVarsReveal) {
		roles = append(roles, connectorOpeningRole)
	}
	return roles
}

func (p *Provider) forgetConnectorGrants(ctx context.Context, report providerkit.Reporter) error {
	return everyStep(
		func() error { return p.bindConnectorProject(ctx, false) },
		func() error { return p.bindConnectorKeys(ctx, nil, report) },
	)
}

func (p *Provider) bindConnectorProject(ctx context.Context, granting bool) error {
	clients, err := p.stood(ctx)
	if err != nil {
		return err
	}
	member := "serviceAccount:" + clients.ConnectorAccountEmail()
	if err := clients.bindProjectRole(ctx, member, connectorRecordsRole, databaseCondition(clients.project, clients.Namespace()), granting); err != nil {
		return fmt.Errorf("let %s read and write the %s database's records: %w", member, ports.Database(clients.Namespace()), err)
	}
	return nil
}

func (p *Provider) bindConnectorKeys(ctx context.Context, wanted []string, report providerkit.Reporter) error {
	clients, err := p.stood(ctx)
	if err != nil {
		return err
	}
	member := "serviceAccount:" + clients.ConnectorAccountEmail()
	var errs []error
	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		changed, err := clients.bindKeyRoles(ctx, class, member, connectorKeyRoles, wanted)
		if err != nil && len(wanted) > 0 {
			return err
		}
		if changed && report != nil && len(wanted) > 0 {
			report.Say("The connector holds " + strings.Join(wanted, " and ") + " on the " + string(class) + " key")
		}
		errs = append(errs, err)
	}
	return errors.Join(errs...)
}

var connectorKeyRoles = []string{connectorSealingRole, connectorOpeningRole}

func boundMembers(members []string, member string, granting bool) ([]string, bool) {
	at := slices.Index(members, member)
	switch {
	case granting && at >= 0:
		return members, false
	case granting:
		return append(members, member), true
	case at < 0:
		return members, false
	default:
		return slices.Delete(members, at, at+1), true
	}
}

func (p *Provider) takeConnectorAccount(ctx context.Context, report providerkit.Reporter) error {
	clients, err := p.stood(ctx)
	if err != nil {
		return err
	}
	service, err := clients.Accounts()
	if err != nil {
		return err
	}
	name := clients.Connector()
	if _, err := attempted(ctx, service.Projects.ServiceAccounts.Delete(
		accountPath(clients, name)).Context(ctx).Do); err != nil && !absent(err) {
		return fmt.Errorf("delete the %s service account: %w", name, err)
	}
	if report != nil {
		report.Say("Took away " + clients.ConnectorAccountEmail())
	}
	return nil
}

func (p *Provider) takeConnectorImages(ctx context.Context, report providerkit.Reporter) error {
	clients, err := p.stood(ctx)
	if err != nil {
		return err
	}
	service, err := clients.Repositories()
	if err != nil {
		return err
	}
	held := repositoryPath(clients, clients.Repository(providerkit.ClassProduction)) +
		"/packages/" + connectorImageName
	if _, err := attempted(ctx, service.Projects.Locations.Repositories.Packages.Delete(
		held).Context(ctx).Do); err != nil && !absent(err) {
		return fmt.Errorf("delete the connector images at %s: %w", held, err)
	}
	if report != nil {
		report.Say("Took away the connector images")
	}
	return nil
}

func hostOf(held string) string {
	if held == "" {
		return ""
	}
	parsed, err := url.Parse(held)
	if err != nil {
		return ""
	}
	return parsed.Host
}
