package gcp

import (
	"context"
	"fmt"
	"slices"
	"strings"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/pkg/refusal"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/bootstrap"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/gcloud"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/grants"
	"github.com/ocelhq/ocel/platform/gcp/provider/internal/names"
	"github.com/ocelhq/ocel/platform/gcp/provider/resources/variables"
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
	} else {
		token, err := c.Tokens.Token(ctx)
		if err != nil {
			return provider.Principal{}, gcloud.Unauthenticated()
		}
		name, err := c.principal(ctx, token)
		if err != nil {
			return provider.Principal{}, err
		}
		principal.Name = name
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

func (c Credentials) principal(ctx context.Context, token string) (string, error) {
	endpoint := c.TokenInfoURL
	if endpoint == "" {
		endpoint = gcloud.TokenInfoURL
	}
	return gcloud.PrincipalNamed(ctx, endpoint, token)
}

func (c Credentials) Permissions(purpose edge.CredentialPurpose, tier environment.Tier) (edge.CredentialDocument, error) {
	switch purpose {
	case edge.PurposeBootstrap, edge.PurposeDeploy:
		if purpose == edge.PurposeDeploy && tier.Sibling() == "" {
			return edge.CredentialDocument{}, refusal.Refuse(refusal.CodeInvalid,
				"a deploy credential is fenced to one tier, production or preview, and this request named %q", tier)
		}
		project, err := c.Project.Project(context.Background())
		if err != nil {
			return edge.CredentialDocument{}, err
		}
		names := names.New(c.Namespace, project)
		roles := slices.Clone(bootstrap.RolesFor(purpose))
		if purpose == edge.PurposeDeploy {
			roles = append(roles, bootstrap.RealtimeSecretsGrant(names), bootstrap.GateSecretsGrant(names), bootstrap.ValueSecretsGrant(names), grants.AppGrantsGrant(names), names.AppAccountsRolePath(), bootstrap.AppBucketsGrant(names), bootstrap.DatabasesGrant(names), bootstrap.ProjectPermissionsGrant(), bootstrap.PartsGrant(names), variables.ProjectRecordsGrant(names, c.Region), bootstrap.TierTagsGrant(names, tier))
			roles = append(roles, bootstrap.QueueGrants(names, c.Region)...)
			roles = append(roles, bootstrap.UntaggingGrants(names, c.Region)...)
		}
		credential := string(purpose) + " credential"
		if purpose == edge.PurposeDeploy {
			credential += " for the " + string(tier) + " tier"
		}
		heading := fmt.Sprintf("the roles a %s is granted on project %s, where %s is the number `gcloud projects describe %s --format='value(projectNumber)'` prints",
			credential, project, bootstrap.ProjectNumberHole, project)
		return edge.CredentialDocument{
			Heading:  heading,
			Document: strings.Join(roles, "\n"),
		}, nil
	default:
		return edge.CredentialDocument{}, refusal.Refuse(refusal.CodeInvalid,
			"credential permissions are rendered for bootstrap or deploy credentials; this request named neither")
	}
}

var _ provider.Credentials = Credentials{}
