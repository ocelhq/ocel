package gcp

import (
	"context"
	"fmt"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/credential"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
)

type ProjectSource interface {
	Project(ctx context.Context) (string, error)
}

type Named string

func (n Named) Project(context.Context) (string, error) { return string(n), nil }

type Credentials struct {
	Project ProjectSource
	Region  string

	Tokens gcloud.TokenSource

	TokenInfoURL string

	Endpoint string

	Projects Projects

	Namespace provider.Namespace
}

func (c Credentials) Whoami(ctx context.Context) (provider.Principal, error) {
	project, err := c.Project.Project(ctx)
	if err != nil {
		return provider.Principal{}, err
	}
	principal := provider.Principal{
		Vendor:   gcloud.Vendor,
		Account:  project,
		Name:     gcloud.EmulatorPrincipal,
		Location: c.Region,
	}
	if c.Endpoint != "" {
		principal.Details = []provider.PrincipalDetail{{Label: "emulator", Value: c.Endpoint}}
	}
	if principal.Name, err = c.PrincipalName(ctx); err != nil {
		return provider.Principal{}, err
	}
	if c.Projects == nil {
		return provider.Principal{}, refusal.Refuse(refusal.CodeDenied,
			"nothing here can ask whether this credential reaches project %s, and a credential nothing vouched for deploys nothing", project)
	}
	if err := c.Projects.Reaches(ctx, project); err != nil {
		return provider.Principal{}, err
	}
	return principal, nil
}

func (c Credentials) PrincipalName(ctx context.Context) (string, error) {
	if c.Endpoint != "" {
		return gcloud.EmulatorPrincipal, nil
	}
	token, err := c.Tokens.Token(ctx)
	if err != nil {
		return "", gcloud.Unauthenticated()
	}
	return c.principal(ctx, token)
}

func (c Credentials) principal(ctx context.Context, token string) (string, error) {
	endpoint := c.TokenInfoURL
	if endpoint == "" {
		endpoint = gcloud.TokenInfoURL
	}
	return gcloud.PrincipalNamed(ctx, endpoint, token)
}

func (c Credentials) Permissions(purpose edge.CredentialPurpose, tier environment.Tier) (edge.CredentialDocument, error) {
	if purpose != edge.PurposeBootstrap && purpose != edge.PurposeDeploy {
		return edge.CredentialDocument{}, refusal.Refuse(refusal.CodeInvalid,
			"credential permissions are rendered for bootstrap or deploy credentials; this request named neither")
	}
	project, err := c.Project.Project(context.Background())
	if err != nil {
		return edge.CredentialDocument{}, err
	}
	if purpose == edge.PurposeBootstrap {
		return edge.CredentialDocument{
			Heading:  fmt.Sprintf("the roles a bootstrap credential is granted on project %s", project),
			Document: strings.Join(bootstrap.BootstrapRoles(), "\n"),
		}, nil
	}
	if tier != environment.TierProduction && tier != environment.TierPreview {
		return edge.CredentialDocument{}, refusal.Refuse(refusal.CodeInvalid,
			"a deploy credential is rendered for the production or the preview tier, and this request named %q", tier)
	}
	names := names.New(c.Namespace, project)
	deploy := credential.New(names, c.Region, tier)
	return edge.CredentialDocument{
		Heading: fmt.Sprintf("the roles a deploy credential of the %s tier is granted on project %s, whose %s resources carry the %s tag; make each custom role named by its permissions yourself",
			tier, project, tier, names.TierTagValuePath(tier)),
		Document: strings.Join(credential.Lines(deploy.Grants()), "\n"),
	}, nil
}

var _ provider.Credentials = Credentials{}
