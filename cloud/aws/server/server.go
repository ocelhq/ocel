// Package server implements the AWS provider's DeploymentService: it drives
// real provisioning (Deploy) and account bootstrap (Bootstrap) behind the
// provider protocol. It delegates resource translation to the deploy
// package, account-global setup to the bootstrap package, and the Pulumi
// runtime to pulumirt, so this package stays a thin protocol adapter.
package server

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	connect "connectrpc.com/connect"

	"github.com/aws/aws-sdk-go-v2/aws"
	awsconfig "github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/ocelhq/ocel/cloud/aws/bootstrap"
	"github.com/ocelhq/ocel/cloud/aws/deploy"
	"github.com/ocelhq/ocel/cloud/aws/pulumirt"
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
)

// deployEnv is the environment segment of a production project's Pulumi stack
// name (stacks are "<project_id>-prod").
const deployEnv = "prod"

// pulumiProjectName is the fixed Pulumi project all Ocel stacks live under.
const pulumiProjectName = "ocel"

// Server implements deploymentsv1connect.DeploymentServiceHandler. It serves every
// RPC in the contract, so it no longer embeds the generated Unimplemented
// handler.
type Server struct{}

// stackName derives the Pulumi stack name for a deploy/destroy from the project
// and the resolved environment. It is pure. A production, unspecified, or nil
// environment keeps the historical "<projectID>-prod"; a preview environment is
// isolated into its own "<projectID>-preview-<identity>" stack (identity is
// already substrate-safe from the CLI).
func stackName(projectID string, env *deploymentsv1.Environment) string {
	return projectID + "-" + envSegment(env)
}

// envSegment is the environment token shared by the Pulumi stack name and the
// asset-bucket key: "preview-<identity>" for a preview, else "prod".
func envSegment(env *deploymentsv1.Environment) string {
	if env.GetClass() == deploymentsv1.Environment_CLASS_PREVIEW {
		return "preview-" + env.GetIdentity()
	}
	return deployEnv
}

// options is the provider's opaque per-invocation options, decoded from the
// request's options JSON. Region is optional: when empty, AWS's own
// resolution (AWS_REGION / shared config) applies.
type options struct {
	Region string `json:"region"`
}

func parseOptions(raw []byte) (options, error) {
	var o options
	if len(raw) == 0 {
		return o, nil
	}
	if err := json.Unmarshal(raw, &o); err != nil {
		return o, fmt.Errorf("parse provider options: %w", err)
	}
	return o, nil
}

// Deploy provisions the manifest's resources and streams progress, ending in
// a terminal ResultEvent that carries the whole-stack connection outputs. A
// malformed manifest is rejected up front as an InvalidArgument error;
// provisioning failures arrive as a terminal ResultEvent with success=false.
func (s *Server) Deploy(ctx context.Context, req *deploymentsv1.DeployRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	manifest := req.GetManifest()
	if err := validateManifest(manifest); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	progress := func(m string) { _ = stream.Send(progressEvent(m)) }
	logf := func(m string) { _ = stream.Send(logEvent(m)) }

	outputs, err := s.runDeploy(ctx, req, manifest, progress, logf)
	if err != nil {
		return stream.Send(resultEvent(false, err.Error(), nil))
	}
	return stream.Send(resultEvent(true, "", outputs))
}

func (s *Server) runDeploy(ctx context.Context, req *deploymentsv1.DeployRequest, manifest *deploymentsv1.Manifest, progress, logf func(string)) ([]*deploymentsv1.ResourceOutput, error) {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return nil, err
	}
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return nil, err
	}
	cfn := cloudformation.NewFromConfig(awscfg)
	ssmClient := ssm.NewFromConfig(awscfg)

	env := req.GetEnvironment()
	preview := env.GetClass() == deploymentsv1.Environment_CLASS_PREVIEW
	bootstrapCmd := "ocel bootstrap"
	if preview {
		bootstrapCmd = "ocel bootstrap --preview"
	}

	// A preview deploy must read (and write) the preview substrate's Pulumi
	// backend, not production's — otherwise `ocel preview rm`/`ls`, which
	// resolve the preview backend, never see what up just created.
	progress("Checking account bootstrap")
	deployed, err := checkBootstrap(ctx, cfn, preview)
	if err != nil {
		return nil, err
	}
	if err := bootstrap.CheckCompat(deployed.Version, deployed.Present, bootstrap.RequiredBootstrapVersion).Explain(); err != nil {
		return nil, err
	}
	if deployed.StateBucket == "" {
		return nil, fmt.Errorf("account bootstrap is present but its state bucket is missing (a partial rollback?); re-run `%s`", bootstrapCmd)
	}
	if deployed.ArtifactBucket == "" {
		return nil, fmt.Errorf("account bootstrap is present but its artifact bucket is missing (a partial rollback?); re-run `%s`", bootstrapCmd)
	}
	if deployed.AssetBucket == "" {
		return nil, fmt.Errorf("account bootstrap is present but its asset bucket is missing (a partial rollback?); re-run `%s`", bootstrapCmd)
	}

	passphrase, err := bootstrap.ReadPassphrase(ctx, ssmClient)
	if err != nil {
		return nil, err
	}

	pulumiCmd, err := pulumirt.Ensure(ctx, progress)
	if err != nil {
		return nil, err
	}

	for _, r := range manifest.GetResources() {
		logf(resourceSummary(r))
	}

	// A bucket resource scopes its IAM grants to the account-global sessions
	// table, so resolve that table's ARN (account + region) up front.
	var stateTableARN string
	if manifestHasBucket(manifest) {
		if deployed.StateTable == "" {
			return nil, fmt.Errorf("account bootstrap is present but its sessions table is missing; re-run `%s`", bootstrapCmd)
		}
		account, err := accountID(ctx, sts.NewFromConfig(awscfg))
		if err != nil {
			return nil, err
		}
		stateTableARN = fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/%s", awscfg.Region, account, deployed.StateTable)
	}

	return deploy.Run(ctx, deploy.Config{
		Region:           awscfg.Region,
		BackendURL:       "s3://" + deployed.StateBucket,
		Passphrase:       passphrase,
		ProjectName:      pulumiProjectName,
		StackName:        stackName(manifest.GetProjectId(), env),
		Pulumi:           pulumiCmd,
		Secrets:          secretsmanager.NewFromConfig(awscfg),
		StateTable:     deployed.StateTable,
		StateTableARN:  stateTableARN,
		ListenerCodePath: listenerCodePath,
		ArtifactRoot:     artifactRoot(),
		ArtifactBucket:   deployed.ArtifactBucket,
		AssetBucket:      deployed.AssetBucket,
		Env:              envSegment(env),
		Uploader:         s3.NewFromConfig(awscfg),
		Cloudflare:       deploy.NewCloudflareDeployer(),
		Lifecycle:        env.GetLifecycle(),
		Identity:         env.GetIdentity(),
		ExpiresAt:        previewExpiry(env.GetLifecycle(), time.Now()),
	}, manifest, progress, logf)
}

// previewTTL is how long an ephemeral preview lives before it is considered
// expired — surfaced by `ocel preview ls` and, later, reaped. Ephemeral
// previews are cheap and recoverable, so a week balances "still there when I
// come back to the PR" against "cleaned up before it leaks".
const previewTTL = 7 * 24 * time.Hour

// previewExpiry returns the epoch-seconds expiry to stamp on a deploy: now +
// previewTTL for an ephemeral preview, 0 (no expiry) for every other lifecycle.
// It is pure.
func previewExpiry(lifecycle deploymentsv1.Environment_Lifecycle, now time.Time) int64 {
	if lifecycle != deploymentsv1.Environment_LIFECYCLE_EPHEMERAL {
		return 0
	}
	return now.Add(previewTTL).Unix()
}

// Bootstrap creates the account-global resources the provider needs and
// streams progress, ending in a terminal ResultEvent. Bootstrap carries no
// outputs.
func (s *Server) Bootstrap(ctx context.Context, req *deploymentsv1.BootstrapRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	progress := func(m string) { _ = stream.Send(progressEvent(m)) }
	logf := func(m string) { _ = stream.Send(logEvent(m)) }

	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return stream.Send(resultEvent(false, err.Error(), nil))
	}
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return stream.Send(resultEvent(false, err.Error(), nil))
	}
	cfn := cloudformation.NewFromConfig(awscfg)
	ssmClient := ssm.NewFromConfig(awscfg)

	preview := req.GetClass() == deploymentsv1.Environment_CLASS_PREVIEW

	// The version gate runs on every invocation, bootstrap included. Here a
	// missing or older bootstrap is expected — it's exactly what this call
	// creates or upgrades — so only a bootstrap NEWER than this provider
	// understands is fatal (upgrade the CLI rather than downgrade the account).
	// Each substrate has its own stack, so the gate reads the one being acted on.
	deployed, err := checkBootstrap(ctx, cfn, preview)
	if err != nil {
		return stream.Send(resultEvent(false, err.Error(), nil))
	}
	if bootstrap.CheckCompat(deployed.Version, deployed.Present, bootstrap.RequiredBootstrapVersion) == bootstrap.NeedsCLIUpgrade {
		return stream.Send(resultEvent(false, bootstrap.NeedsCLIUpgrade.Explain().Error(), nil))
	}

	run := bootstrap.Run
	if preview {
		run = bootstrap.RunPreview
	}
	if err := run(ctx, cfn, ssmClient, progress, logf); err != nil {
		return stream.Send(resultEvent(false, err.Error(), nil))
	}
	return stream.Send(resultEvent(true, "", nil))
}

// checkBootstrap reads the deployed state of the substrate a command acts on:
// the preview infrastructure when preview is true, else the production one.
func checkBootstrap(ctx context.Context, api bootstrap.CFNDescriber, preview bool) (bootstrap.Deployed, error) {
	if preview {
		return bootstrap.CheckDeployedPreview(ctx, api)
	}
	return bootstrap.CheckDeployed(ctx, api)
}

// listenerCodePathEnvVar names the built listener-Lambda handler archive the
// bucket deploy path uploads. It is set by the provider's distribution workflow
// (which packages the handler binary alongside the provider) — deferred with
// provider publish; empty until then, which is fine while prod is not made live.
const listenerCodePathEnvVar = "OCEL_LISTENER_CODE_PATH"

// listenerCodePath returns the configured listener archive path, or "" when
// distribution has not wired one yet.
var listenerCodePath = os.Getenv(listenerCodePathEnvVar)

// artifactRootDirName is the project-relative directory a ManifestFunction's
// artifact_path is rooted at: the app builder writes each `.func` under
// <project>/.ocel/output, and artifact_path is relative to it.
const artifactRootDirName = ".ocel/output"

// artifactRoot is the absolute directory function artifacts resolve against.
// The CLI spawns the provider from the project root, so the project's
// .ocel/output lives under the working directory; a failure to read it yields
// the relative path, still valid from that same working directory.
func artifactRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return artifactRootDirName
	}
	return filepath.Join(wd, artifactRootDirName)
}

// STSAPI is the read subset of the STS client the bucket deploy path uses to
// resolve the account id for the sessions-table ARN.
type STSAPI interface {
	GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

func accountID(ctx context.Context, api STSAPI) (string, error) {
	out, err := api.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return "", fmt.Errorf("resolve AWS account id: %w", err)
	}
	return aws.ToString(out.Account), nil
}

// manifestHasBucket reports whether any resource in the manifest is a bucket.
func manifestHasBucket(m *deploymentsv1.Manifest) bool {
	for _, r := range m.GetResources() {
		if r.GetBucket() != nil {
			return true
		}
	}
	return false
}

// loadAWS resolves AWS configuration from the standard default chain,
// overriding the region only when one was supplied in the provider options.
func loadAWS(ctx context.Context, region string) (aws.Config, error) {
	var loadOpts []func(*awsconfig.LoadOptions) error
	if region != "" {
		loadOpts = append(loadOpts, awsconfig.WithRegion(region))
	}
	return awsconfig.LoadDefaultConfig(ctx, loadOpts...)
}

// resourceSummary renders the typed config the provider decoded for a
// manifest resource, e.g. "postgres_main: postgres version=15". Emitted as a
// log line so a caller driving the real binary can observe the exact typed
// value that reached the provider.
func resourceSummary(r *deploymentsv1.ManifestResource) string {
	switch cfg := r.GetConfig().(type) {
	case *deploymentsv1.ManifestResource_Postgres:
		return fmt.Sprintf("%s: postgres version=%s", r.GetLogicalName(), cfg.Postgres.GetVersion())
	case *deploymentsv1.ManifestResource_Bucket:
		return fmt.Sprintf("%s: bucket allowed_origins=%v", r.GetLogicalName(), cfg.Bucket.GetAllowedOrigins())
	default:
		return fmt.Sprintf("%s: received config", r.GetLogicalName())
	}
}

func progressEvent(message string) *deploymentsv1.DeployEvent {
	return &deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: message}},
	}
}

func logEvent(message string) *deploymentsv1.DeployEvent {
	return &deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Log{Log: &deploymentsv1.LogEvent{Message: message}},
	}
}

func resultEvent(success bool, errMsg string, outputs []*deploymentsv1.ResourceOutput) *deploymentsv1.DeployEvent {
	return &deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{
			Success: success,
			Error:   errMsg,
			Outputs: outputs,
		}},
	}
}

// validateManifest reports whether manifest is well-formed enough for the
// provider to act on: a schema version and project id are set, and every
// resource entry carries a logical name, a typed resource identifier, and a
// typed config.
func validateManifest(m *deploymentsv1.Manifest) error {
	if m == nil {
		return fmt.Errorf("manifest is required")
	}
	if m.GetSchemaVersion() == "" {
		return fmt.Errorf("manifest.schema_version is required")
	}
	if m.GetProjectId() == "" {
		return fmt.Errorf("manifest.project_id is required")
	}
	for i, r := range m.GetResources() {
		if r.GetLogicalName() == "" {
			return fmt.Errorf("manifest.resources[%d]: logical_name is required", i)
		}
		if r.GetResource() == nil || r.GetResource().GetType() == resourcesv1.ResourceType_RESOURCE_TYPE_UNSPECIFIED {
			return fmt.Errorf("manifest.resources[%d] (%s): a valid resource type is required", i, r.GetLogicalName())
		}
		if r.GetConfig() == nil {
			return fmt.Errorf("manifest.resources[%d] (%s): typed config is required", i, r.GetLogicalName())
		}
	}
	return nil
}
