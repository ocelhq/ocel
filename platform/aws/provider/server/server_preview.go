package server

import (
	"context"
	"errors"
	"fmt"
	"os"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/ocelhq/ocel/pkg/naming"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/pulumiruntime"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

var errPreviewInfraMissing = errors.New("preview infrastructure is not set up; run `ocel bootstrap --preview` first")

func (s *Server) Preflight(ctx context.Context, req *deploymentsv1.PreflightRequest) (*deploymentsv1.PreflightResponse, error) {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return nil, err
	}

	resp := &deploymentsv1.PreflightResponse{Identity: &deploymentsv1.Identity{}}

	awsOK := true
	if id, err := s.callerIdentity(ctx, sts.NewFromConfig(awscfg), opts.Region); err != nil {
		awsOK = false
		resp.CredentialProblems = append(resp.CredentialProblems, &deploymentsv1.CredentialProblem{
			Provider: "AWS",
			Message:  fmt.Sprintf("could not authenticate: %v", err),
			Hint:     "configure AWS credentials (set AWS_PROFILE, run `aws sso login`, or export access keys)",
		})
	} else {
		resp.Identity.AwsAccount = id.account
		resp.Identity.AwsArn = id.arn
		resp.Identity.AwsRegion = awscfg.Region
		resp.Identity.AwsProfile = os.Getenv("AWS_PROFILE")
	}

	resp.DomainClaims = domainClaims(ctx, s.edgeRouteOwner(), req.GetSlug(), req.GetDomains())

	if v, ok := s.edge().(edge.CredentialVerifier); ok {
		if id, err := v.VerifyCredentials(ctx); err != nil {
			resp.CredentialProblems = append(resp.CredentialProblems, &deploymentsv1.CredentialProblem{
				Provider: "Cloudflare",
				Message:  err.Error(),
				Hint:     "set CLOUDFLARE_API_TOKEN and CLOUDFLARE_ACCOUNT_ID to a token with access to that account",
			})
		} else {
			resp.Identity.CloudflareAccount = id.Account
		}
	}

	if awsOK {
		cfn := cloudformation.NewFromConfig(awscfg)
		preview, production, err := s.preflightSubstrates(ctx, cfn, opts.Region, req.GetRequiredClass())
		if err != nil {
			return nil, err
		}

		pf := preflightResponse(req.GetRequiredClass(), preview, production)
		resp.InfraClass = pf.GetInfraClass()
		resp.InfrastructurePresent = pf.GetInfrastructurePresent()

		if req.GetRequiredClass() == deploymentsv1.Environment_CLASS_PRODUCTION {
			resp.KnownSlugs = knownSlugs(ctx, awscfg, production, req.GetSlug())
		}
	}

	return resp, nil
}

type routeOwnerFunc func(ctx context.Context, pattern string) (string, error)

func (s *Server) edgeRouteOwner() routeOwnerFunc {
	return s.edge().(edge.RootStack).RouteOwner
}

func domainClaims(ctx context.Context, owner routeOwnerFunc, slug string, domains []string) []*deploymentsv1.DomainClaim {
	if len(domains) == 0 {
		return nil
	}
	claims := make([]*deploymentsv1.DomainClaim, 0, len(domains))
	for _, hostname := range domains {
		claim := &deploymentsv1.DomainClaim{Hostname: hostname}
		claims = append(claims, claim)
		script, err := owner(ctx, cloudflare.RoutePattern(hostname))
		switch {
		case err != nil:
			continue
		case script == "" || deploy.ProjectOwnsWorker(slug, script):
			claim.Status = deploymentsv1.DomainClaim_STATUS_UNCLAIMED
		default:
			claim.Status = deploymentsv1.DomainClaim_STATUS_CLAIMED
			claim.Owner = script
		}
	}
	return claims
}

func knownSlugs(ctx context.Context, awscfg aws.Config, substrate bootstrap.Deployed, slug string) []string {
	if slug == "" || !substrate.Present {
		return nil
	}
	index, err := stackIndexFor(awscfg, substrate, bootstrapCommand(false))
	if err != nil {
		return nil
	}
	slugs, err := deploy.ProjectSlugsBesides(ctx, index, slug)
	if err != nil {
		return nil
	}
	return slugs
}

func (s *Server) preflightSubstrates(ctx context.Context, cfn bootstrap.CFNDescriber, region string, required deploymentsv1.Environment_Class) (preview, production bootstrap.Deployed, err error) {
	previewRequired := required == deploymentsv1.Environment_CLASS_PREVIEW

	wanted, err := s.deployed(ctx, cfn, region, previewRequired)
	if err != nil {
		return bootstrap.Deployed{}, bootstrap.Deployed{}, err
	}
	var other bootstrap.Deployed
	if !wanted.Present {
		if other, err = s.deployed(ctx, cfn, region, !previewRequired); err != nil {
			return bootstrap.Deployed{}, bootstrap.Deployed{}, err
		}
	}
	if previewRequired {
		return wanted, other, nil
	}
	return other, wanted, nil
}

func preflightResponse(required deploymentsv1.Environment_Class, preview, production bootstrap.Deployed) *deploymentsv1.PreflightResponse {
	wanted, other := requiredSubstrate(required, preview, production)
	switch {
	case wanted.Present:
		return &deploymentsv1.PreflightResponse{InfraClass: classToEnum(wanted.Class), InfrastructurePresent: true}
	case other.Present:
		return &deploymentsv1.PreflightResponse{InfraClass: classToEnum(other.Class), InfrastructurePresent: true}
	default:
		return &deploymentsv1.PreflightResponse{InfraClass: deploymentsv1.Environment_CLASS_UNSPECIFIED, InfrastructurePresent: false}
	}
}

func requiredSubstrate(required deploymentsv1.Environment_Class, preview, production bootstrap.Deployed) (wanted, other bootstrap.Deployed) {
	if required == deploymentsv1.Environment_CLASS_PREVIEW {
		return preview, production
	}
	return production, preview
}

func classToEnum(class string) deploymentsv1.Environment_Class {
	switch class {
	case bootstrap.ClassProduction:
		return deploymentsv1.Environment_CLASS_PRODUCTION
	case bootstrap.ClassPreview:
		return deploymentsv1.Environment_CLASS_PREVIEW
	default:
		return deploymentsv1.Environment_CLASS_UNSPECIFIED
	}
}

func (s *Server) DestroyPreview(ctx context.Context, req *deploymentsv1.DestroyPreviewRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	progress := func(m string) { _ = stream.Send(progressEvent(m)) }
	logf := func(m string) { _ = stream.Send(logEvent(m)) }

	if err := s.runDestroyPreview(ctx, req, progress, logf); err != nil {
		return stream.Send(failureResult(err))
	}
	return stream.Send(okResult())
}

func (s *Server) runDestroyPreview(ctx context.Context, req *deploymentsv1.DestroyPreviewRequest, progress, logf func(string)) error {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return err
	}
	env := req.GetEnvironment()
	pointer, err := deploy.EnvName(env)
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	cfg, stack, state, err := s.previewTeardownContext(ctx, opts, req.GetSlug(), env)
	if err != nil {
		return err
	}

	persistent := env.GetLifecycle() == deploymentsv1.Environment_LIFECYCLE_PERSISTENT
	return deploy.RemovePreview(ctx, stack, state, cfg, req.GetSlug(), pointer, persistent, progress, logf)
}

func (s *Server) previewTeardownContext(ctx context.Context, opts options, slug string, env *deploymentsv1.Environment) (deploy.Config, edge.RootStack, edge.RootStackState, error) {
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return deploy.Config{}, nil, nil, err
	}
	cfn := cloudformation.NewFromConfig(awscfg)
	ssmClient := ssm.NewFromConfig(awscfg)

	deployed, err := s.deployed(ctx, cfn, opts.Region, true)
	if err != nil {
		return deploy.Config{}, nil, nil, err
	}
	if !deployed.Present || deployed.StateBucket == "" {
		return deploy.Config{}, nil, nil, errPreviewInfraMissing
	}
	stacks, err := stackIndexFor(awscfg, deployed, bootstrapCommand(true))
	if err != nil {
		return deploy.Config{}, nil, nil, err
	}

	params, err := bootstrap.ReadTeardownParams(ctx, ssmClient, bootstrap.ClassPreview, slug)
	if err != nil {
		return deploy.Config{}, nil, nil, err
	}
	if params.PassphraseErr != nil {
		return deploy.Config{}, nil, nil, params.PassphraseErr
	}
	pulumiCmd, err := pulumiruntime.Ensure(ctx, nil)
	if err != nil {
		return deploy.Config{}, nil, nil, err
	}

	stack, ok := s.edge().(edge.RootStack)
	if !ok {
		return deploy.Config{}, nil, nil, fmt.Errorf("this edge does not support the root stack")
	}

	envName, err := deploy.EnvScope(env)
	if err != nil {
		return deploy.Config{}, nil, nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	cfg := deploy.Config{
		Region:             awscfg.Region,
		BackendURL:         naming.StateBackendURL(deployed.StateBucket, slug),
		Passphrase:         params.Passphrase,
		PulumiProject:      naming.PulumiProject(slug),
		Pulumi:             pulumiCmd,
		AssetBucket:        deployed.AssetBucket,
		ArtifactBucket:     deployed.ArtifactBucket,
		Uploader:           s3.NewFromConfig(awscfg),
		CacheStoreBucket:   params.CacheStore.Bucket,
		CacheStoreUploader: cacheStoreUploader(params.CacheStore),
		Stacks:             stacks,
		Env:                envName,
		Slug:               slug,

		ISRWriterEndpoint:      params.ISRWriter.Endpoint,
		ISRWriterBootstrapCred: params.ISRWriter.BootstrapCred,

		Values: teardownValues(awscfg, deployed, bootstrap.ClassPreview),
	}
	return cfg, stack, params.RootStackState, nil
}

func (s *Server) ListEnvironments(ctx context.Context, req *deploymentsv1.ListEnvironmentsRequest) (*deploymentsv1.ListEnvironmentsResponse, error) {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return nil, err
	}
	cfn := cloudformation.NewFromConfig(awscfg)

	deployed, err := s.deployed(ctx, cfn, opts.Region, true)
	if err != nil {
		return nil, err
	}
	if !deployed.Present || deployed.StateTable == "" {
		return &deploymentsv1.ListEnvironmentsResponse{}, nil
	}
	index, err := stackIndexFor(awscfg, deployed, bootstrapCommand(true))
	if err != nil {
		return nil, err
	}

	stacks, err := deploy.ListPreviewStacks(ctx, index, req.GetSlug())
	if err != nil {
		return nil, err
	}
	return &deploymentsv1.ListEnvironmentsResponse{Environments: toPreviewEnvironments(stacks)}, nil
}

func toPreviewEnvironments(stacks []deploy.PreviewStack) []*deploymentsv1.PreviewEnvironment {
	out := make([]*deploymentsv1.PreviewEnvironment, 0, len(stacks))
	for _, s := range stacks {
		out = append(out, &deploymentsv1.PreviewEnvironment{
			Identity:  s.Identity,
			Lifecycle: s.Lifecycle,
			Label:     s.Label,
			CreatedAt: s.CreatedAt,
			ExpiresAt: s.ExpiresAt,
		})
	}
	return out
}
