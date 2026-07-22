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

	"github.com/ocelhq/ocel/cloud/aws/bootstrap"
	"github.com/ocelhq/ocel/cloud/aws/deploy"
	"github.com/ocelhq/ocel/cloud/aws/pulumirt"
	"github.com/ocelhq/ocel/cloud/edge"
	"github.com/ocelhq/ocel/cloud/edge/cloudflare"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
)

// errPreviewInfraMissing is returned when a preview teardown or list is asked
// for but no preview infrastructure exists in the account.
var errPreviewInfraMissing = errors.New("preview infrastructure is not set up; run `ocel bootstrap --preview` first")

// Preflight authenticates the ambient credentials this provider needs (its own
// AWS credentials and the Cloudflare edge's), reports what they resolve to for
// the CLI's "Running with:" banner, and — when the AWS credentials
// authenticated — reports the class of the infrastructure present. The CLI
// calls it before a preview or deploy so a missing or invalid credential is
// refused before provisioning rather than part way through. Credential failures
// are returned in-band (credential_problems) so every one is reported at once;
// only a transport/read fault returns an error.
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

	// AWS: authenticate via STS. On failure the account's infrastructure can't
	// be read, so the infra check below is skipped and the CLI aborts on the
	// reported problem.
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

	// Cloudflare edge: verify through the edge seam. Every production deploy
	// reconciles the root stack on the edge, so its credentials are always
	// required.
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
	}

	return resp, nil
}

// preflightResponse maps the discovered substrates to a PreflightResponse for
// the class the caller requires. It is pure. The substrate matching required
// wins when present, so an account with both substrates gates each command
// against the right one; when the required substrate is absent but the other
// exists, the other is reported so the caller's class guard fires an
// informative mismatch; an empty account is reported absent.
func preflightResponse(required deploymentsv1.Environment_Class, preview, production bootstrap.Deployed) *deploymentsv1.PreflightResponse {
	wanted, other := production, preview
	if required == deploymentsv1.Environment_CLASS_PREVIEW {
		wanted, other = preview, production
	}
	switch {
	case wanted.Present:
		return &deploymentsv1.PreflightResponse{InfraClass: classToEnum(wanted.Class), InfrastructurePresent: true}
	case other.Present:
		return &deploymentsv1.PreflightResponse{InfraClass: classToEnum(other.Class), InfrastructurePresent: true}
	default:
		return &deploymentsv1.PreflightResponse{InfraClass: deploymentsv1.Environment_CLASS_UNSPECIFIED, InfrastructurePresent: false}
	}
}

// classToEnum maps a bootstrap class marker to the provider contract enum.
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

// DestroyPreview tears down the preview environment addressed by req.Environment
// and streams progress, ending in a terminal ResultEvent. It reuses the
// DeployEvent stream.
func (s *Server) DestroyPreview(ctx context.Context, req *deploymentsv1.DestroyPreviewRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	progress := func(m string) { _ = stream.Send(progressEvent(m)) }
	logf := func(m string) { _ = stream.Send(logEvent(m)) }

	if err := s.runDestroyPreview(ctx, req, progress, logf); err != nil {
		return stream.Send(resultEvent(false, err.Error(), nil, nil))
	}
	return stream.Send(resultEvent(true, "", nil, nil))
}

// runDestroyPreview tears one preview pointer down in full (ADR 0001): every
// app-deploy stack the pointer's builds live in, the pointer and its records in
// the preview store, the pointer's R2/S3 assets, and — for a persistent preview
// only — its per-name infra stack. It resolves the preview substrate's backend
// and root-stack state, then drives deploy.RemovePreview. Best-effort like
// DestroyProject; it retains nothing.
func (s *Server) runDestroyPreview(ctx context.Context, req *deploymentsv1.DestroyPreviewRequest, progress, logf func(string)) error {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return err
	}
	env := req.GetEnvironment()
	cfg, stack, state, err := s.previewTeardownContext(ctx, opts, req.GetProjectId(), env)
	if err != nil {
		return err
	}

	pointer := env.GetIdentity()
	persistent := env.GetLifecycle() == deploymentsv1.Environment_LIFECYCLE_PERSISTENT
	return deploy.RemovePreview(ctx, stack, state, cfg, req.GetProjectId(), pointer, persistent, progress, logf)
}

// previewTeardownContext resolves everything a preview teardown (rm/prune/
// destroy) needs against the preview substrate: the reclaim Config (preview
// Pulumi backend, preview cache store, the env segment for the pointer), the
// preview root stack, and the project's persisted preview root-stack state. A
// missing preview substrate is refused up front. state is nil when the project
// never deployed a preview — the caller treats that as "nothing store-side to
// remove", not an error.
func (s *Server) previewTeardownContext(ctx context.Context, opts options, projectID string, env *deploymentsv1.Environment) (deploy.Config, edge.RootStack, edge.RootStackState, error) {
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
	pulumiCmd, err := pulumirt.Ensure(ctx, nil)
	if err != nil {
		return deploy.Config{}, nil, nil, err
	}

	// Best-effort, like the production prune's read: a preview whose edge never
	// adopted a cache store reclaims nothing from CacheStoreBucket.
	cacheStore, err := bootstrap.ReadCacheStore(ctx, ssmClient, bootstrap.ClassPreview)
	if err != nil {
		cacheStore = bootstrap.CacheStore{}
	}

	state, err := bootstrap.ReadRootStackStateFor(ctx, ssmClient, bootstrap.ClassPreview, projectID)
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
		Uploader:           s3.NewFromConfig(awscfg),
		CacheStoreBucket:   cacheStore.Bucket,
		CacheStoreUploader: cacheStoreUploader(cacheStore),
		Env:                envSegment(env),
		Slug:               env.GetIdentity(),
	}
	return cfg, stack, state, nil
}

// ListEnvironments enumerates the preview environments from the preview
// substrate's Pulumi backend. An account with no preview infrastructure lists
// nothing rather than erroring.
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
	pulumiCmd, err := pulumirt.Ensure(ctx, nil)
	if err != nil {
		return nil, err
	}

	stacks, err := deploy.ListPreviewStacks(ctx, deploy.ListConfig{
		Region:      awscfg.Region,
		BackendURL:  "s3://" + deployed.StateBucket,
		Passphrase:  passphrase,
		ProjectName: pulumiProjectName,
		ProjectID:   req.GetProjectId(),
		Pulumi:      pulumiCmd,
	})
	if err != nil {
		return nil, err
	}
	return &deploymentsv1.ListEnvironmentsResponse{Environments: toPreviewEnvironments(stacks)}, nil
}

// toPreviewEnvironments maps enumerated preview stacks to the proto reply. It is
// pure.
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
