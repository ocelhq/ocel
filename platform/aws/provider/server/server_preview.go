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

	previewClass := req.GetRequiredClass() == deploymentsv1.Environment_CLASS_PREVIEW
	if previewClass {
		if err := deploy.PreviewLabelProblem(req.GetSlug(), req.GetDomains()); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
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

	resp.DomainClaims = domainClaims(ctx, s.edgeRouteOwner(edge.Kind(req.GetEdgeKind()), awscfg.Region), req.GetSlug(), req.GetDomains())

	edgeFront, err := s.edge(edge.Kind(req.GetEdgeKind()), awscfg.Region)
	if err != nil {
		return nil, err
	}
	if v, ok := edgeFront.(edge.CredentialVerifier); ok {
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

		if previewClass && preview.Present {
			recorded, err := bootstrap.ReadPreviewDomain(ctx, ssm.NewFromConfig(awscfg), bootstrap.ClassPreview)
			if err != nil {
				return nil, err
			}
			if err := globalPreviewProblem(recorded, req); err != nil {
				return nil, connect.NewError(connect.CodeFailedPrecondition, err)
			}
			if recorded.BaseDomain != "" {
				owner, err := s.globalPreviewOwner(recorded, awscfg.Region)
				if err != nil {
					return nil, connect.NewError(connect.CodeFailedPrecondition, err)
				}
				resp.GlobalPreviewDomain = globalPreviewDomain(ctx, owner, recorded)
				resp.KnownSlugs = knownSlugs(ctx, awscfg, preview, req.GetSlug())
			}
		}
	}

	return resp, nil
}

func globalPreviewProblem(recorded bootstrap.PreviewDomain, req *deploymentsv1.PreflightRequest) error {
	servesOnSharedWildcard := req.GetSlug() != "" && len(req.GetDomains()) == 0
	if !servesOnSharedWildcard {
		return nil
	}
	return globalPreviewAccountMismatch(recorded)
}

type routeOwnerFunc func(ctx context.Context, hostname string) (string, error)

func (s *Server) edgeRouteOwner(kind edge.Kind, region string) routeOwnerFunc {
	return func(ctx context.Context, hostname string) (string, error) {
		edgeFront, err := s.edge(kind, region)
		if err != nil {
			return "", err
		}
		return edgeFront.DomainOwner(ctx, hostname)
	}
}

func domainClaims(ctx context.Context, owner routeOwnerFunc, slug string, domains []string) []*deploymentsv1.DomainClaim {
	if len(domains) == 0 {
		return nil
	}
	claims := make([]*deploymentsv1.DomainClaim, 0, len(domains))
	for _, hostname := range domains {
		claim := &deploymentsv1.DomainClaim{Hostname: hostname}
		claims = append(claims, claim)
		script, err := owner(ctx, hostname)
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

func (s *Server) DestroyPreview(ctx context.Context, req *deploymentsv1.DestroyPreviewRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) (err error) {
	sender := newEventSender(ctx, stream.Send)
	defer func() { err = sender.close() }()
	tracer := newEventTracer(sender)
	stageReport, logf := newTeardownReporter(sender)

	if derr := s.runDestroyPreview(ctx, req, tracer, stageReport, logf); derr != nil {
		sender.send(failureResult(derr))
		return nil
	}
	sender.send(okResult())
	return nil
}

func newPreviewRemovalStages(persistent bool) deploy.PreviewRemovalStages {
	stages := deploy.PreviewRemovalStages{
		Pointer: deploy.NewRootStage("Removing the preview pointer"),
		Reclaim: deploy.NewRootStage("Reclaiming deployments"),
	}
	if persistent {
		stages.Infra = deploy.NewRootStage("Destroying the preview infra stack")
	}
	return stages
}

func (s *Server) runDestroyPreview(ctx context.Context, req *deploymentsv1.DestroyPreviewRequest, tracer deploy.Tracer, stageReport func(deploy.StageID) func(string), logf func(string)) error {
	env := req.GetEnvironment()
	persistent := env.GetLifecycle() == deploymentsv1.Environment_LIFECYCLE_PERSISTENT

	stages := newPreviewRemovalStages(persistent)
	deploy.DeclareStages(tracer, false, stages.Roots()...)
	finish := func(err error) error {
		if err != nil {
			closeStages(tracer)
		}
		return err
	}

	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return finish(err)
	}
	pointer, err := deploy.EnvName(env)
	if err != nil {
		return finish(connect.NewError(connect.CodeInvalidArgument, err))
	}
	cfg, stack, err := s.previewTeardownContext(ctx, edge.Kind(req.GetEdgeKind()), opts, req.GetSlug(), env)
	if err != nil {
		return finish(err)
	}
	cfg.Tracer = tracer
	cfg.StageReport = stageReport

	return deploy.RemovePreview(ctx, stack, cfg, req.GetSlug(), pointer, persistent, stages, logf)
}

func (s *Server) previewTeardownContext(ctx context.Context, kind edge.Kind, opts options, slug string, env *deploymentsv1.Environment) (deploy.Config, edge.EdgeStack, error) {
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return deploy.Config{}, nil, err
	}
	cfn := cloudformation.NewFromConfig(awscfg)
	ssmClient := ssm.NewFromConfig(awscfg)

	deployed, err := s.deployed(ctx, cfn, opts.Region, true)
	if err != nil {
		return deploy.Config{}, nil, err
	}
	if !deployed.Present || deployed.StateBucket == "" {
		return deploy.Config{}, nil, errPreviewInfraMissing
	}
	stacks, err := stackIndexFor(awscfg, deployed, bootstrapCommand(true))
	if err != nil {
		return deploy.Config{}, nil, err
	}

	params, err := bootstrap.ReadTeardownParams(ctx, ssmClient, bootstrap.ClassPreview, slug)
	if err != nil {
		return deploy.Config{}, nil, err
	}
	if params.PassphraseErr != nil {
		return deploy.Config{}, nil, params.PassphraseErr
	}
	pulumiCmd, err := pulumiruntime.Ensure(ctx, nil)
	if err != nil {
		return deploy.Config{}, nil, err
	}

	envName, err := deploy.EnvScope(env)
	if err != nil {
		return deploy.Config{}, nil, connect.NewError(connect.CodeInvalidArgument, err)
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
	edgeFront, err := s.edge(kind, awscfg.Region)
	if err != nil {
		return deploy.Config{}, nil, err
	}
	stack, err := edgeFront.Open(params.StackState)
	if err != nil {
		return deploy.Config{}, nil, err
	}
	return cfg, stack, nil
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
