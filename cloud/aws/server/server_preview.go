package server

import (
	"context"
	"errors"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/ssm"

	"github.com/ocelhq/ocel/cloud/aws/bootstrap"
	"github.com/ocelhq/ocel/cloud/aws/deploy"
	"github.com/ocelhq/ocel/cloud/aws/pulumirt"
	providerv1 "github.com/ocelhq/ocel/pkg/proto/provider/v1"
)

// errPreviewInfraMissing is returned when a preview teardown or list is asked
// for but no preview infrastructure exists in the account.
var errPreviewInfraMissing = errors.New("preview infrastructure is not set up; run `ocel bootstrap --preview` first")

// Preflight reports what the provider's ambient account/profile points at: the
// class of the infrastructure present and whether any is present, so the CLI
// can refuse a preview or deploy locally before provisioning. It reads both the
// preview and production substrates and maps them via preflightResponse.
func (s *Server) Preflight(ctx context.Context, req *providerv1.PreflightRequest) (*providerv1.PreflightResponse, error) {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return nil, connect.NewError(connect.CodeInvalidArgument, err)
	}
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return nil, err
	}
	cfn := cloudformation.NewFromConfig(awscfg)

	preview, err := bootstrap.CheckDeployedPreview(ctx, cfn)
	if err != nil {
		return nil, err
	}
	production, err := bootstrap.CheckDeployed(ctx, cfn)
	if err != nil {
		return nil, err
	}
	return preflightResponse(req.GetRequiredClass(), preview, production), nil
}

// preflightResponse maps the discovered substrates to a PreflightResponse for
// the class the caller requires. It is pure. The substrate matching required
// wins when present, so an account with both substrates gates each command
// against the right one; when the required substrate is absent but the other
// exists, the other is reported so the caller's class guard fires an
// informative mismatch; an empty account is reported absent.
func preflightResponse(required providerv1.Environment_Class, preview, production bootstrap.Deployed) *providerv1.PreflightResponse {
	wanted, other := production, preview
	if required == providerv1.Environment_CLASS_PREVIEW {
		wanted, other = preview, production
	}
	switch {
	case wanted.Present:
		return &providerv1.PreflightResponse{InfraClass: classToEnum(wanted.Class), InfrastructurePresent: true}
	case other.Present:
		return &providerv1.PreflightResponse{InfraClass: classToEnum(other.Class), InfrastructurePresent: true}
	default:
		return &providerv1.PreflightResponse{InfraClass: providerv1.Environment_CLASS_UNSPECIFIED, InfrastructurePresent: false}
	}
}

// classToEnum maps a bootstrap class marker to the provider contract enum.
func classToEnum(class string) providerv1.Environment_Class {
	switch class {
	case bootstrap.ClassProduction:
		return providerv1.Environment_CLASS_PRODUCTION
	case bootstrap.ClassPreview:
		return providerv1.Environment_CLASS_PREVIEW
	default:
		return providerv1.Environment_CLASS_UNSPECIFIED
	}
}

// Destroy tears down the preview environment addressed by req.Environment and
// streams progress, ending in a terminal ResultEvent. It reuses the DeployEvent
// stream.
func (s *Server) Destroy(ctx context.Context, req *providerv1.DestroyRequest, stream *connect.ServerStream[providerv1.DeployEvent]) error {
	progress := func(m string) { _ = stream.Send(progressEvent(m)) }
	logf := func(m string) { _ = stream.Send(logEvent(m)) }

	if err := s.runDestroy(ctx, req, progress, logf); err != nil {
		return stream.Send(resultEvent(false, err.Error(), nil))
	}
	return stream.Send(resultEvent(true, "", nil))
}

func (s *Server) runDestroy(ctx context.Context, req *providerv1.DestroyRequest, progress, logf func(string)) error {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return err
	}
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return err
	}
	cfn := cloudformation.NewFromConfig(awscfg)
	ssmClient := ssm.NewFromConfig(awscfg)

	progress("Checking preview infrastructure")
	deployed, err := bootstrap.CheckDeployedPreview(ctx, cfn)
	if err != nil {
		return err
	}
	if !deployed.Present || deployed.StateBucket == "" {
		return errPreviewInfraMissing
	}

	passphrase, err := bootstrap.ReadPassphrase(ctx, ssmClient)
	if err != nil {
		return err
	}
	pulumiCmd, err := pulumirt.Ensure(ctx, progress)
	if err != nil {
		return err
	}

	return deploy.Destroy(ctx, deploy.TeardownConfig{
		Region:      awscfg.Region,
		BackendURL:  "s3://" + deployed.StateBucket,
		Passphrase:  passphrase,
		ProjectName: pulumiProjectName,
		StackName:   stackName(req.GetProjectId(), req.GetEnvironment()),
		Pulumi:      pulumiCmd,
	}, progress, logf)
}

// ListEnvironments enumerates the preview environments from the preview
// substrate's Pulumi backend. An account with no preview infrastructure lists
// nothing rather than erroring.
func (s *Server) ListEnvironments(ctx context.Context, req *providerv1.ListEnvironmentsRequest) (*providerv1.ListEnvironmentsResponse, error) {
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
		return &providerv1.ListEnvironmentsResponse{}, nil
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
	return &providerv1.ListEnvironmentsResponse{Environments: toPreviewEnvironments(stacks)}, nil
}

// toPreviewEnvironments maps enumerated preview stacks to the proto reply. It is
// pure.
func toPreviewEnvironments(stacks []deploy.PreviewStack) []*providerv1.PreviewEnvironment {
	out := make([]*providerv1.PreviewEnvironment, 0, len(stacks))
	for _, s := range stacks {
		out = append(out, &providerv1.PreviewEnvironment{
			Identity:  s.Identity,
			Lifecycle: s.Lifecycle,
			Label:     s.Label,
			CreatedAt: s.CreatedAt,
			ExpiresAt: s.ExpiresAt,
		})
	}
	return out
}
