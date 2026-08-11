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
	if id, err := sts.NewFromConfig(awscfg).GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{}); err != nil {
		awsOK = false
		resp.CredentialProblems = append(resp.CredentialProblems, &deploymentsv1.CredentialProblem{
			Provider: "AWS",
			Message:  fmt.Sprintf("could not authenticate: %v", err),
			Hint:     "configure AWS credentials (set AWS_PROFILE, run `aws sso login`, or export access keys)",
		})
	} else {
		resp.Identity.AwsAccount = aws.ToString(id.Account)
		resp.Identity.AwsArn = aws.ToString(id.Arn)
		resp.Identity.AwsRegion = awscfg.Region
		resp.Identity.AwsProfile = os.Getenv("AWS_PROFILE")
	}

	resp.DomainClaims = domainClaims(ctx, edgeRouteOwner(), req.GetSlug(), req.GetDomains())

	if v, ok := cloudflare.New().(edge.CredentialVerifier); ok {
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
		preview, err := bootstrap.CheckDeployedPreview(ctx, cfn)
		if err != nil {
			return nil, err
		}
		production, err := bootstrap.CheckDeployed(ctx, cfn)
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

func edgeRouteOwner() routeOwnerFunc {
	return cloudflare.New().(edge.RootStack).RouteOwner
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
	if slug == "" || !substrate.Present || substrate.StateBucket == "" {
		return nil
	}
	passphrase, err := bootstrap.ReadPassphrase(ctx, ssm.NewFromConfig(awscfg))
	if err != nil {
		return nil
	}
	pulumiCmd, err := pulumiruntime.Ensure(ctx, nil)
	if err != nil {
		return nil
	}
	slugs, err := deploy.ProjectSlugsBesides(ctx, deploy.ListConfig{
		Region:      awscfg.Region,
		BackendURL:  "s3://" + substrate.StateBucket,
		Passphrase:  passphrase,
		ProjectName: pulumiProjectName,
		Slug:        slug,
		Pulumi:      pulumiCmd,
	})
	if err != nil {
		return nil
	}
	return slugs
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
	cfg, stack, state, err := s.previewTeardownContext(ctx, opts, req.GetSlug(), env)
	if err != nil {
		return err
	}

	pointer := env.GetIdentity()
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

	deployed, err := bootstrap.CheckDeployedPreview(ctx, cfn)
	if err != nil {
		return deploy.Config{}, nil, nil, err
	}
	if !deployed.Present || deployed.StateBucket == "" {
		return deploy.Config{}, nil, nil, errPreviewInfraMissing
	}

	passphrase, err := bootstrap.ReadPassphrase(ctx, ssmClient)
	if err != nil {
		return deploy.Config{}, nil, nil, err
	}
	pulumiCmd, err := pulumiruntime.Ensure(ctx, nil)
	if err != nil {
		return deploy.Config{}, nil, nil, err
	}

	cacheStore, err := bootstrap.ReadCacheStore(ctx, ssmClient, bootstrap.ClassPreview)
	if err != nil {
		cacheStore = bootstrap.CacheStore{}
	}

	isrWriter, err := bootstrap.ReadISRWriterFor(ctx, ssmClient, bootstrap.ClassPreview)
	if err != nil {
		isrWriter = bootstrap.ISRWriter{}
	}

	state, err := bootstrap.ReadRootStackStateFor(ctx, ssmClient, bootstrap.ClassPreview, slug)
	if err != nil {
		return deploy.Config{}, nil, nil, err
	}
	stack, ok := cloudflare.New().(edge.RootStack)
	if !ok {
		return deploy.Config{}, nil, nil, fmt.Errorf("this edge does not support the root stack")
	}

	cfg := deploy.Config{
		Region:             awscfg.Region,
		BackendURL:         "s3://" + deployed.StateBucket,
		Passphrase:         passphrase,
		ProjectName:        pulumiProjectName,
		Pulumi:             pulumiCmd,
		AssetBucket:        deployed.AssetBucket,
		ArtifactBucket:     deployed.ArtifactBucket,
		Uploader:           s3.NewFromConfig(awscfg),
		CacheStoreBucket:   cacheStore.Bucket,
		CacheStoreUploader: cacheStoreUploader(cacheStore),
		Env:                envSegment(env),
		Slug:               env.GetIdentity(),

		ISRWriterEndpoint:      isrWriter.Endpoint,
		ISRWriterBootstrapCred: isrWriter.BootstrapCred,

		Values: teardownValues(awscfg, deployed, bootstrap.ClassPreview),
	}
	return cfg, stack, state, nil
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
	ssmClient := ssm.NewFromConfig(awscfg)

	deployed, err := bootstrap.CheckDeployedPreview(ctx, cfn)
	if err != nil {
		return nil, err
	}
	if !deployed.Present || deployed.StateBucket == "" {
		return &deploymentsv1.ListEnvironmentsResponse{}, nil
	}

	passphrase, err := bootstrap.ReadPassphrase(ctx, ssmClient)
	if err != nil {
		return nil, err
	}
	pulumiCmd, err := pulumiruntime.Ensure(ctx, nil)
	if err != nil {
		return nil, err
	}

	stacks, err := deploy.ListPreviewStacks(ctx, deploy.ListConfig{
		Region:      awscfg.Region,
		BackendURL:  "s3://" + deployed.StateBucket,
		Passphrase:  passphrase,
		ProjectName: pulumiProjectName,
		Slug:        req.GetSlug(),
		Pulumi:      pulumiCmd,
	})
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
