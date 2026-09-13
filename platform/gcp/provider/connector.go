package gcp

import (
	"archive/tar"
	"bytes"
	"context"
	"fmt"
	"io"
	"net/url"
	"slices"
	"time"

	"cloud.google.com/go/iam/apiv1/iampb"
	"cloud.google.com/go/kms/apiv1/kmspb"
	v1 "github.com/google/go-containerregistry/pkg/v1"
	"github.com/google/go-containerregistry/pkg/v1/mutate"
	"github.com/google/go-containerregistry/pkg/v1/tarball"
	"google.golang.org/api/cloudresourcemanager/v1"
	"google.golang.org/api/googleapi"
	"google.golang.org/api/iam/v1"
	run "google.golang.org/api/run/v2"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/ocelhq/ocel/pkg/naming"
	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/pkg/target"
	"github.com/ocelhq/ocel/platform/gcp/provider/ports"
)

const (
	ConnectorArch = "amd64"

	connectorImageName    = "ocel-connector"
	connectorImagePath    = "/connector"
	connectorMemory       = 256
	connectorInstances    = 1
	connectorTimeout      = 30 * time.Second
	connectorVersionEnv   = "OCEL_CONNECTOR_VERSION"
	connectorRecordsRole  = "roles/datastore.user"
	connectorSealingRole  = "roles/cloudkms.cryptoKeyEncrypterDecrypter"
	connectorAccountNote  = "the identity the ocel connector answers the console as"
	connectorBindAttempts = 4

	conditionalPolicyVersion = 3
)

var _ providerkit.ConnectorHost = (*Provider)(nil)

const connectorCompute = providerkit.ComputeServerless

func connectorComputeOf(asked providerkit.Compute) (providerkit.Compute, error) {
	if asked == "" || asked == connectorCompute {
		return connectorCompute, nil
	}
	return "", providerkit.Refuse(providerkit.CodeInvalid,
		"this project runs the connector on a Cloud Run service that scales to nothing, which is %s; %s is not yet built for gcp",
		connectorCompute, asked)
}

func (p *Provider) DescribeConnectorTarget(ctx context.Context) (providerkit.ConnectorTarget, error) {
	names := p.Names()
	if err := names.connectorFits(); err != nil {
		return providerkit.ConnectorTarget{}, err
	}
	fingerprint, err := target.ForGCP(p.options.Project, p.options.Region, string(names.Namespace()))
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
	described.Installed = &providerkit.ConnectorRelease{Version: connectorVersionOf(standing), Compute: connectorCompute}
	return described, nil
}

func (p *Provider) InstallConnector(ctx context.Context, install providerkit.ConnectorInstall,
	report providerkit.Reporter) (providerkit.ConnectorAddress, error) {
	compute, err := connectorComputeOf(install.Compute)
	if err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	names := p.Names()
	if err := names.connectorFits(); err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	if len(install.Config) == 0 {
		return providerkit.ConnectorAddress{}, providerkit.Refuse(providerkit.CodeInvalid,
			"this install carries no connector config, so nothing would name the console the service trusts")
	}
	if p.clients.emulated() {
		return providerkit.ConnectorAddress{}, providerkit.Refuse(providerkit.CodeNotReady,
			"this run talks to an emulator, which stands up no Cloud Run service and hands out no url a console could dial: add the connector against the project itself")
	}

	image, err := p.pushedConnector(ctx, install.Binary, report)
	if err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	if err := p.standConnectorAccount(ctx, report); err != nil {
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
		env: map[string]string{
			providerkit.NamespaceEnvVar:       string(names.Namespace()),
			ports.ProjectEnvVar:               p.options.Project,
			ports.RegionEnvVar:                p.options.Region,
			connectorVersionEnv:               install.Version,
			providerkit.ConnectorConfigEnvVar: string(install.Config),
		},
	}, report)
	if err != nil {
		return providerkit.ConnectorAddress{}, err
	}
	if released.url == "" {
		return providerkit.ConnectorAddress{}, providerkit.Refuse(providerkit.CodeNotReady,
			"%s stands and published no url, so the console has nothing to dial", names.Connector())
	}
	return providerkit.ConnectorAddress{URL: released.url, Compute: compute}, nil
}

func (p *Provider) RemoveConnector(ctx context.Context, report providerkit.Reporter) error {
	names := p.Names()
	if err := p.tearDown(ctx, names.Connector(), report); err != nil {
		return err
	}
	if err := p.forgetConnectorGrants(ctx, report); err != nil {
		return err
	}
	if err := p.takeConnectorAccount(ctx, report); err != nil {
		return err
	}
	return p.takeConnectorImages(ctx, report)
}

func (p *Provider) connectorService(ctx context.Context) (*run.GoogleCloudRunV2Service, error) {
	services, err := p.clients.Run()
	if err != nil {
		return nil, err
	}
	path := p.servicePath(p.Names().Connector())
	held, err := attempted(ctx, func(call ...googleapi.CallOption) (*run.GoogleCloudRunV2Service, error) {
		return services.Projects.Locations.Services.Get(path).Context(ctx).Do(call...)
	})
	if absent(err) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("read the Cloud Run service %s: %w", p.Names().Connector(), err)
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
	ref := p.Names().RepositoryPath(p.options.Region, providerkit.ClassProduction) +
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

func (p *Provider) standConnectorAccount(ctx context.Context, report providerkit.Reporter) error {
	names := p.Names()
	service, err := p.clients.Accounts()
	if err != nil {
		return err
	}
	_, err = attempted(ctx, service.Projects.ServiceAccounts.Create("projects/"+p.options.Project,
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
	return p.bindConnectorKeys(ctx, true, report)
}

func (p *Provider) forgetConnectorGrants(ctx context.Context, report providerkit.Reporter) error {
	if err := p.bindConnectorProject(ctx, false); err != nil {
		return err
	}
	return p.bindConnectorKeys(ctx, false, report)
}

func (p *Provider) bindConnectorProject(ctx context.Context, granting bool) error {
	service, err := p.clients.Projects()
	if err != nil {
		return err
	}
	member := "serviceAccount:" + p.Names().ConnectorAccountEmail()
	onlyThisDatabase := databaseCondition(p.options.Project, p.Names().Namespace())
	var refused error
	for attempt := range connectorBindAttempts {
		if attempt > 0 && !waited(ctx, attempt) {
			return ctx.Err()
		}
		policy, err := attempted(ctx, service.Projects.GetIamPolicy(p.options.Project,
			&cloudresourcemanager.GetIamPolicyRequest{
				Options: &cloudresourcemanager.GetPolicyOptions{RequestedPolicyVersion: conditionalPolicyVersion},
			}).Context(ctx).Do)
		if err != nil {
			return fmt.Errorf("read who may read this project's records: %w", err)
		}
		bindings, changed := boundMember(policy.Bindings, connectorRecordsRole, member, onlyThisDatabase, granting)
		if !changed {
			return nil
		}
		policy.Bindings = bindings
		policy.Version = conditionalPolicyVersion
		_, refused = attempted(ctx, service.Projects.SetIamPolicy(p.options.Project,
			&cloudresourcemanager.SetIamPolicyRequest{Policy: policy}).Context(ctx).Do)
		if refused == nil {
			return nil
		}
		if !taken(refused) {
			break
		}
	}
	return fmt.Errorf("let %s read and write the %s database's records: %w",
		member, ports.Database(p.Names().Namespace()), refused)
}

func databaseCondition(project string, ns providerkit.Namespace) *cloudresourcemanager.Expr {
	return &cloudresourcemanager.Expr{
		Title: "ocel " + string(ns) + " database",
		Expression: fmt.Sprintf("resource.name == %q",
			fmt.Sprintf("projects/%s/databases/%s", project, ports.Database(ns))),
	}
}

func sameCondition(held, want *cloudresourcemanager.Expr) bool {
	if held == nil || want == nil {
		return held == nil && want == nil
	}
	return held.Expression == want.Expression
}

func (p *Provider) bindConnectorKeys(ctx context.Context, granting bool, report providerkit.Reporter) error {
	client, err := p.clients.KMS()
	if err != nil {
		return err
	}
	member := "serviceAccount:" + p.Names().ConnectorAccountEmail()
	for _, class := range []providerkit.Class{providerkit.ClassProduction, providerkit.ClassPreview} {
		key := keyPath(p.clients, string(class))
		if _, err := dialled(ctx, func() (*kmspb.CryptoKey, error) {
			return client.GetCryptoKey(ctx, &kmspb.GetCryptoKeyRequest{Name: key})
		}); err != nil {
			if status.Code(err) == codes.NotFound {
				continue
			}
			return fmt.Errorf("read the %s key the connector opens values under: %w", class, err)
		}
		policy, err := dialled(ctx, func() (*iampb.Policy, error) {
			return client.GetIamPolicy(ctx, &iampb.GetIamPolicyRequest{Resource: key})
		})
		if err != nil {
			return fmt.Errorf("read who may seal under the %s key: %w", class, err)
		}
		bindings, changed := boundKeyMember(policy.GetBindings(), connectorSealingRole, member, granting)
		if !changed {
			continue
		}
		policy.Bindings = bindings
		if _, err := dialled(ctx, func() (*iampb.Policy, error) {
			return client.SetIamPolicy(ctx, &iampb.SetIamPolicyRequest{Resource: key, Policy: policy})
		}); err != nil {
			return fmt.Errorf("let %s seal and open %s values: %w", member, class, err)
		}
		if report != nil && granting {
			report.Say("The connector may seal and open " + string(class) + " values")
		}
	}
	return nil
}

func boundMember(bindings []*cloudresourcemanager.Binding, role, member string,
	condition *cloudresourcemanager.Expr, granting bool) ([]*cloudresourcemanager.Binding, bool) {
	for _, binding := range bindings {
		if binding.Role != role || !sameCondition(binding.Condition, condition) {
			continue
		}
		at := slices.Index(binding.Members, member)
		switch {
		case granting && at >= 0:
			return bindings, false
		case granting:
			binding.Members = append(binding.Members, member)
			return bindings, true
		case at < 0:
			return bindings, false
		default:
			binding.Members = slices.Delete(binding.Members, at, at+1)
			return bindings, true
		}
	}
	if !granting {
		return bindings, false
	}
	return append(bindings, &cloudresourcemanager.Binding{
		Role:      role,
		Members:   []string{member},
		Condition: condition,
	}), true
}

func boundKeyMember(bindings []*iampb.Binding, role, member string, granting bool) ([]*iampb.Binding, bool) {
	for _, binding := range bindings {
		if binding.GetRole() != role {
			continue
		}
		at := slices.Index(binding.GetMembers(), member)
		switch {
		case granting && at >= 0:
			return bindings, false
		case granting:
			binding.Members = append(binding.GetMembers(), member)
			return bindings, true
		case at < 0:
			return bindings, false
		default:
			binding.Members = slices.Delete(binding.GetMembers(), at, at+1)
			return bindings, true
		}
	}
	if !granting {
		return bindings, false
	}
	return append(bindings, &iampb.Binding{Role: role, Members: []string{member}}), true
}

func (p *Provider) takeConnectorAccount(ctx context.Context, report providerkit.Reporter) error {
	service, err := p.clients.Accounts()
	if err != nil {
		return err
	}
	name := p.Names().Connector()
	if _, err := attempted(ctx, service.Projects.ServiceAccounts.Delete(
		accountPath(p.clients, name)).Context(ctx).Do); err != nil && !absent(err) {
		return fmt.Errorf("delete the %s service account: %w", name, err)
	}
	if report != nil {
		report.Say("Took away " + p.Names().ConnectorAccountEmail())
	}
	return nil
}

func (p *Provider) takeConnectorImages(ctx context.Context, report providerkit.Reporter) error {
	service, err := p.clients.Repositories()
	if err != nil {
		return err
	}
	held := repositoryPath(p.clients, p.Names().Repository(providerkit.ClassProduction)) +
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
