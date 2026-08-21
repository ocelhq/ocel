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
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/pulumiruntime"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

var errPreviewInfraMissing = errors.New("preview infrastructure is not set up; run `ocel bootstrap --preview` first")

func (s *Server) Preflight(ctx context.Context, req *contractv1.PreflightRequest) (*contractv1.PreflightResponse, error) {
	opts := s.config.get()
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return nil, err
	}

	previewTier := req.GetRequiredTier() == environmentv1.Tier_TIER_PREVIEW
	if previewTier {
		if err := deploy.PreviewLabelProblem(req.GetSlug(), req.GetDomains()); err != nil {
			return nil, connect.NewError(connect.CodeInvalidArgument, err)
		}
	}

	resp := &contractv1.PreflightResponse{Identity: &contractv1.Identity{}}

	awsOK := true
	if id, err := s.callerIdentity(ctx, sts.NewFromConfig(awscfg), opts.Region); err != nil {
		awsOK = false
		resp.CredentialProblems = append(resp.CredentialProblems, &contractv1.CredentialProblem{
			Provider: "AWS",
			Message:  fmt.Sprintf("could not authenticate: %v", err),
			Hint:     "configure AWS credentials (set AWS_PROFILE, run `aws sso login`, or export access keys)",
		})
	} else {
		resp.Identity.Provider = "AWS"
		resp.Identity.Account = id.account
		resp.Identity.Principal = id.principal()
		resp.Identity.Details = identityDetails(awscfg.Region, os.Getenv("AWS_PROFILE"))
	}

	resp.DomainClaims = domainClaims(ctx, s.edgeRouteOwner(requestedEdge(req), awscfg.Region), req.GetSlug(), req.GetDomains())

	edgeFront, err := s.edge(requestedEdge(req), awscfg.Region)
	if err != nil {
		return nil, err
	}
	if v, ok := edgeFront.(edge.CredentialVerifier); ok {
		if id, err := v.VerifyCredentials(ctx); err != nil {
			resp.CredentialProblems = append(resp.CredentialProblems, &contractv1.CredentialProblem{
				Provider: string(edgeFront.Kind()),
				Message:  err.Error(),
				Hint:     fmt.Sprintf("give this run credentials the %s edge accepts", edgeFront.Kind()),
			})
		} else {
			resp.Identity.EdgeScope = id.Account
		}
	}

	if awsOK {
		cfn := cloudformation.NewFromConfig(awscfg)
		preview, production, err := s.preflightBootstraps(ctx, cfn, opts.Region, req.GetRequiredTier())
		if err != nil {
			return nil, err
		}

		pf := preflightResponse(req.GetRequiredTier(), preview, production)
		resp.InfraTier = pf.GetInfraTier()
		resp.InfrastructurePresent = pf.GetInfrastructurePresent()

		wanted, _ := requiredBootstrap(req.GetRequiredTier(), preview, production)
		resp.Bootstrap = s.bootstrapStatus(wanted, req.GetRequiredTier(), req.GetRequiredFeatures())
		if wanted.Present {
			compat := bootstrap.CheckCompat(wanted.Schema, true, bootstrap.RequiredSchema)
			if err := compat.Explain(wanted.Schema, bootstrap.RequiredSchema, bootstrapCommand(previewTier)); err != nil {
				return nil, connect.NewError(connect.CodeFailedPrecondition, err)
			}
		}

		if req.GetRequiredTier() == environmentv1.Tier_TIER_PRODUCTION {
			resp.KnownSlugs = knownSlugs(ctx, awscfg, production, req.GetSlug())
		}

		if previewTier && preview.Present {
			recorded, err := bootstrap.ReadPreviewDomain(ctx, ssm.NewFromConfig(awscfg), bootstrap.ClassPreview)
			if err != nil {
				return nil, err
			}
			if err := globalPreviewProblem(recorded, req, edgeFront); err != nil {
				return nil, connect.NewError(connect.CodeFailedPrecondition, err)
			}
			if recorded.BaseDomain != "" {
				resp.PreviewWildcard = previewWildcard(ctx, s.globalPreviewOwner(recorded, awscfg.Region), recorded)
				resp.KnownSlugs = knownSlugs(ctx, awscfg, preview, req.GetSlug())
			}
		}
	}

	return resp, nil
}

func identityDetails(region, profile string) []*contractv1.Detail {
	var details []*contractv1.Detail
	for _, detail := range []*contractv1.Detail{{Label: "region", Value: region}, {Label: "profile", Value: profile}} {
		if detail.GetValue() != "" {
			details = append(details, detail)
		}
	}
	return details
}

func globalPreviewProblem(recorded bootstrap.PreviewDomain, req *contractv1.PreflightRequest, edgeFront edge.Edge) error {
	servesOnSharedWildcard := req.GetSlug() != "" && len(req.GetDomains()) == 0
	if !servesOnSharedWildcard {
		return nil
	}
	if err := globalPreviewEdgeMismatch(recorded, requestedEdge(req)); err != nil {
		return err
	}
	return globalPreviewScopeMismatch(recorded, edgeFront)
}

func globalPreviewEdgeMismatch(recorded bootstrap.PreviewDomain, kind edge.Kind) error {
	holder, ok := recorded.Holder()
	if recorded.BaseDomain == "" || !ok || holder == kind {
		return nil
	}
	return fmt.Errorf(
		"%s is held by the %s edge, but this project is configured for the %s edge: its previews would publish %s routing while the hostname still resolves through %s, so every preview URL would answer 404 — front this project with the %s edge, or declare this project's own domains.preview",
		edge.PreviewWildcard(recorded.BaseDomain), holder, kind, kind, holder, holder,
	)
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

func domainClaims(ctx context.Context, owner routeOwnerFunc, slug string, domains []string) []*contractv1.DomainClaim {
	if len(domains) == 0 {
		return nil
	}
	claims := make([]*contractv1.DomainClaim, 0, len(domains))
	for _, hostname := range domains {
		claim := &contractv1.DomainClaim{Hostname: hostname}
		claims = append(claims, claim)
		script, err := owner(ctx, hostname)
		switch {
		case err != nil:
			continue
		case script == "" || deploy.ProjectOwnsWorker(slug, script):
			claim.Status = contractv1.DomainClaim_STATUS_UNCLAIMED
		default:
			claim.Status = contractv1.DomainClaim_STATUS_CLAIMED
			claim.Owner = script
		}
	}
	return claims
}

func knownSlugs(ctx context.Context, awscfg aws.Config, deployed bootstrap.Deployed, slug string) []string {
	if slug == "" || !deployed.Present {
		return nil
	}
	index, err := stackIndexFor(awscfg, deployed, bootstrapCommand(false))
	if err != nil {
		return nil
	}
	slugs, err := deploy.ProjectSlugsBesides(ctx, index, slug)
	if err != nil {
		return nil
	}
	return slugs
}

func (s *Server) preflightBootstraps(ctx context.Context, cfn bootstrap.CFNDescriber, region string, required environmentv1.Tier) (preview, production bootstrap.Deployed, err error) {
	previewRequired := required == environmentv1.Tier_TIER_PREVIEW

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

func preflightResponse(required environmentv1.Tier, preview, production bootstrap.Deployed) *contractv1.PreflightResponse {
	wanted, other := requiredBootstrap(required, preview, production)
	switch {
	case wanted.Present:
		return &contractv1.PreflightResponse{InfraTier: classToTier(wanted.Class), InfrastructurePresent: true}
	case other.Present:
		return &contractv1.PreflightResponse{InfraTier: classToTier(other.Class), InfrastructurePresent: true}
	default:
		return &contractv1.PreflightResponse{InfraTier: environmentv1.Tier_TIER_UNSPECIFIED, InfrastructurePresent: false}
	}
}

func requiredBootstrap(required environmentv1.Tier, preview, production bootstrap.Deployed) (wanted, other bootstrap.Deployed) {
	if required == environmentv1.Tier_TIER_PREVIEW {
		return preview, production
	}
	return production, preview
}

func classToTier(class string) environmentv1.Tier {
	switch class {
	case bootstrap.ClassProduction:
		return environmentv1.Tier_TIER_PRODUCTION
	case bootstrap.ClassPreview:
		return environmentv1.Tier_TIER_PREVIEW
	default:
		return environmentv1.Tier_TIER_UNSPECIFIED
	}
}

func (s *Server) RemoveEnvironment(ctx context.Context, req *contractv1.RemoveEnvironmentRequest, stream *connect.ServerStream[progressv1.OperationEvent]) (err error) {
	sender := newEventSender(ctx, stream.Send)
	defer func() { err = errors.Join(err, sender.close()) }()
	tracer := newEventTracer(sender)
	stageReport, logf := newTeardownReporter(sender)

	if derr := s.runDestroyPreview(ctx, req, tracer, stageReport, logf); derr != nil {
		return sender.fail(derr)
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

func (s *Server) runDestroyPreview(ctx context.Context, req *contractv1.RemoveEnvironmentRequest, tracer deploy.Tracer, stageReport func(deploy.StageID) func(string), logf func(string)) error {
	env := req.GetEnvironment()
	persistent := env.GetLifecycle() == environmentv1.Lifecycle_LIFECYCLE_PERSISTENT

	stages := newPreviewRemovalStages(persistent)
	deploy.DeclareStages(tracer, false, stages.Roots()...)
	finish := func(err error) error {
		if err != nil {
			closeStages(tracer)
		}
		return err
	}

	opts := s.config.get()
	pointer, err := deploy.EnvName(env)
	if err != nil {
		return finish(connect.NewError(connect.CodeInvalidArgument, err))
	}
	deps, stack, err := s.previewTeardownDeps(ctx, requestedEdge(req), opts, req.GetSlug(), env)
	if err != nil {
		return finish(err)
	}

	return deploy.RemovePreview(ctx, stack, deps.reclamation(reportingWith(tracer, stageReport)), pointer, persistent, stages, logf)
}

func (s *Server) previewTeardownDeps(ctx context.Context, kind edge.Kind, opts providerConfig, slug string, env *environmentv1.Environment) (teardownContext, edge.EdgeStack, error) {
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return teardownContext{}, nil, err
	}
	cfn := cloudformation.NewFromConfig(awscfg)
	ssmClient := ssm.NewFromConfig(awscfg)

	deployed, err := s.deployed(ctx, cfn, opts.Region, true)
	if err != nil {
		return teardownContext{}, nil, err
	}
	if !deployed.Present || deployed.StateBucket == "" {
		return teardownContext{}, nil, errPreviewInfraMissing
	}
	stacks, err := stackIndexFor(awscfg, deployed, bootstrapCommand(true))
	if err != nil {
		return teardownContext{}, nil, err
	}

	params, err := bootstrap.ReadTeardownParams(ctx, ssmClient, bootstrap.ClassPreview, slug)
	if err != nil {
		return teardownContext{}, nil, err
	}
	if params.PassphraseErr != nil {
		return teardownContext{}, nil, params.PassphraseErr
	}
	pulumiCmd, err := pulumiruntime.Ensure(ctx, nil)
	if err != nil {
		return teardownContext{}, nil, err
	}

	envName, err := deploy.EnvScope(env)
	if err != nil {
		return teardownContext{}, nil, connect.NewError(connect.CodeInvalidArgument, err)
	}

	deps := teardownContext{
		teardown: deploy.Teardown{
			Pulumi: deploy.PulumiAccess{
				Region:        awscfg.Region,
				BackendURL:    naming.StateBackendURL(deployed.StateBucket, slug),
				Passphrase:    params.Passphrase,
				PulumiProject: naming.PulumiProject(slug),
				Command:       pulumiCmd,
			},
			Slug:   slug,
			Env:    envName,
			Stacks: stacks,
			Stores: deploy.ObjectStores{
				Uploader:           s3.NewFromConfig(awscfg),
				ArtifactBucket:     deployed.ArtifactBucket,
				AssetBucket:        deployed.AssetBucket,
				CacheStoreBucket:   params.CacheStore.Bucket,
				CacheStoreUploader: cacheStoreUploader(params.CacheStore),
			},
		},
		isrWriter: deploy.ISRWriterAccess{
			Endpoint:      params.ISRWriter.Endpoint,
			BootstrapCred: params.ISRWriter.BootstrapCred,
		},
		values: teardownValues(awscfg, deployed, bootstrap.ClassPreview),
	}
	edgeFront, err := s.edge(kind, awscfg.Region)
	if err != nil {
		return teardownContext{}, nil, err
	}
	stack, err := edgeFront.Open(params.Stack.Edge)
	if err != nil {
		return teardownContext{}, nil, err
	}
	return deps, stack, nil
}

func (s *Server) ListEnvironments(ctx context.Context, req *contractv1.ListEnvironmentsRequest) (*contractv1.ListEnvironmentsResponse, error) {
	return s.listEnvironments(ctx, s.config.get().Region, req.GetSlug())
}

func (s *Server) listEnvironments(ctx context.Context, region, slug string) (*contractv1.ListEnvironmentsResponse, error) {
	awscfg, err := loadAWS(ctx, region)
	if err != nil {
		return nil, err
	}
	cfn := cloudformation.NewFromConfig(awscfg)

	deployed, err := s.deployed(ctx, cfn, region, true)
	if err != nil {
		return nil, err
	}
	if !deployed.Present || deployed.StateTable == "" {
		return &contractv1.ListEnvironmentsResponse{}, nil
	}
	index, err := stackIndexFor(awscfg, deployed, bootstrapCommand(true))
	if err != nil {
		return nil, err
	}

	stacks, err := deploy.ListPreviewStacks(ctx, index, slug)
	if err != nil {
		return nil, err
	}
	return &contractv1.ListEnvironmentsResponse{Environments: toPreviewEnvironments(stacks)}, nil
}

func toPreviewEnvironments(stacks []deploy.PreviewStack) []*contractv1.PreviewEnvironment {
	out := make([]*contractv1.PreviewEnvironment, 0, len(stacks))
	for _, s := range stacks {
		out = append(out, &contractv1.PreviewEnvironment{
			Identity:  s.Identity,
			Lifecycle: s.Lifecycle,
			Label:     s.Label,
			CreatedAt: s.CreatedAt,
			ExpiresAt: s.ExpiresAt,
		})
	}
	return out
}
