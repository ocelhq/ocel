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
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	"github.com/ocelhq/ocel/cloud/aws/bootstrap"
	"github.com/ocelhq/ocel/cloud/aws/deploy"
	"github.com/ocelhq/ocel/cloud/aws/pulumirt"
	"github.com/ocelhq/ocel/cloud/edge/cloudflare"
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
	progress := func(phase deploymentsv1.Phase, m string, current, total uint32) {
		_ = stream.Send(phaseProgressEvent(phase, m, current, total))
	}
	logf := func(m string) { _ = stream.Send(logEvent(m)) }

	outputs, appURLs, err := s.runDeploy(ctx, req, manifest, progress, logf)
	if err != nil {
		return stream.Send(resultEvent(false, err.Error(), nil, nil))
	}
	return stream.Send(resultEvent(true, "", outputs, appURLs))
}

func (s *Server) runDeploy(ctx context.Context, req *deploymentsv1.DeployRequest, manifest *deploymentsv1.Manifest, progress deploy.Progress, logf func(string)) ([]*deploymentsv1.ResourceOutput, []string, error) {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return nil, nil, err
	}
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return nil, nil, err
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
	progress(deploymentsv1.Phase_PHASE_UPLOADING, "Checking account bootstrap", 0, 0)
	deployed, err := checkBootstrap(ctx, cfn, preview)
	if err != nil {
		return nil, nil, err
	}
	if err := bootstrap.CheckCompat(deployed.Version, deployed.Present, bootstrap.RequiredBootstrapVersion).Explain(); err != nil {
		return nil, nil, err
	}
	if deployed.StateBucket == "" {
		return nil, nil, fmt.Errorf("account bootstrap is present but its state bucket is missing (a partial rollback?); re-run `%s`", bootstrapCmd)
	}
	if deployed.ArtifactBucket == "" {
		return nil, nil, fmt.Errorf("account bootstrap is present but its artifact bucket is missing (a partial rollback?); re-run `%s`", bootstrapCmd)
	}
	if deployed.AssetBucket == "" {
		return nil, nil, fmt.Errorf("account bootstrap is present but its asset bucket is missing (a partial rollback?); re-run `%s`", bootstrapCmd)
	}
	if deployed.StateTable == "" {
		return nil, nil, fmt.Errorf("account bootstrap is present but its state table is missing (a partial rollback?); re-run `%s`", bootstrapCmd)
	}

	passphrase, err := bootstrap.ReadPassphrase(ctx, ssmClient)
	if err != nil {
		return nil, nil, err
	}

	// The edge reader credentials are injected into the Next.js worker so it can
	// sign its Function-URL forwards (the Lambdas are AWS_IAM-gated) and read ISR
	// directly from S3+DynamoDB. Read best-effort so an account bootstrapped
	// before edge credentials existed still reaches the deploy — but a worker
	// without them cannot sign, so every Lambda route 403s until bootstrap mints
	// the key. The warning has to name that, not just interception.
	edgeClass := bootstrap.ClassProduction
	if preview {
		edgeClass = bootstrap.ClassPreview
	}
	edgeCreds, err := bootstrap.ReadEdgeCredentials(ctx, ssmClient, edgeClass)
	if err != nil {
		logf("edge reader credentials unavailable: " + err.Error() +
			" — the worker cannot sign its Function-URL forwards, so every route will 403; re-run `" + bootstrapCmd + "` to mint the edge key")
		edgeCreds = bootstrap.EdgeCredentials{}
	}

	edgeValues := readEdgeValues(ctx, ssmClient, edgeClass, bootstrapCmd, logf)

	pulumiCmd, err := pulumirt.Ensure(ctx, func(m string) {
		progress(deploymentsv1.Phase_PHASE_UPLOADING, m, 0, 0)
	})
	if err != nil {
		return nil, nil, err
	}

	for _, r := range manifest.GetResources() {
		logf(resourceSummary(r))
	}

	// Bucket resources and Next cache handlers both scope their IAM grants to
	// the state table, so resolve its ARN up front — unconditionally. Gating
	// this on which resources happen to need it means every new consumer has to
	// remember to widen the gate, and one that forgets renders an empty
	// Resource into its policy and fails the deploy at Pulumi.
	account, err := accountID(ctx, sts.NewFromConfig(awscfg))
	if err != nil {
		return nil, nil, err
	}
	stateTableARN := fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/%s", awscfg.Region, account, deployed.StateTable)

	// The cache-store parameter is named here, not in the Lambda: resolving the
	// substrate class is already this side's job, and the same name has to appear
	// in the function's read grant anyway.
	cacheStoreParam, err := bootstrap.CacheStoreParamFor(edgeClass)
	if err != nil {
		return nil, nil, err
	}
	cacheStoreParamARN := fmt.Sprintf("arn:aws:ssm:%s:%s:parameter%s", awscfg.Region, account, cacheStoreParam)

	// Seeded ISR entries have to land in the same store the deployed cache
	// handler reads, and the handler resolves that from this same parameter. A
	// failed read is therefore fatal rather than best-effort: guessing wrong
	// costs no error, only every prerendered route silently rendering cold.
	cacheStore, err := bootstrap.ReadCacheStore(ctx, ssmClient, edgeClass)
	if err != nil {
		return nil, nil, err
	}

	// Both classes realize the stacked model (ADR 0001): read this substrate's
	// prior root-stack state and its own deployments-store worker coordinates.
	// The two substrates keep separate state (their own store instance, secret
	// and owner token), keyed by class. A bootstrap predating the store reads the
	// zero value, and the deploy then fails fast asking for a re-bootstrap
	// (realize, deploy/production.go).
	priorRootStackState, err := bootstrap.ReadRootStackStateFor(ctx, ssmClient, edgeClass, manifest.GetProjectId())
	if err != nil {
		return nil, nil, err
	}
	deploymentsStore, err := bootstrap.ReadDeploymentsStoreFor(ctx, ssmClient, edgeClass)
	if err != nil {
		return nil, nil, err
	}

	outputs, urls, rootStackState, err := deploy.Run(ctx, deploy.Config{
		Region:        awscfg.Region,
		BackendURL:    "s3://" + deployed.StateBucket,
		Passphrase:    passphrase,
		ProjectName:   pulumiProjectName,
		StackName:     stackName(manifest.GetProjectId(), env),
		Pulumi:        pulumiCmd,
		Secrets:       secretsmanager.NewFromConfig(awscfg),
		StateTable:    deployed.StateTable,
		StateTableARN: stateTableARN,

		CacheStoreParam:    cacheStoreParam,
		CacheStoreParamARN: cacheStoreParamARN,
		CacheStoreBucket:   cacheStore.Bucket,
		CacheStoreUploader: cacheStoreUploader(cacheStore),

		ListenerCodePath: listenerCodePath,
		ArtifactRoot:     artifactRoot(),
		ArtifactBucket:   deployed.ArtifactBucket,
		AssetBucket:      deployed.AssetBucket,
		Env:              envSegment(env),
		EdgeAccessKeyID:  edgeCreds.AccessKeyID,
		EdgeSecretKey:    edgeCreds.SecretAccessKey,
		EdgeValues:       edgeValues,

		Slug:               manifest.GetSlug(),
		StoreScriptName:    deploymentsStore.ScriptName,
		StoreEndpoint:      deploymentsStore.Endpoint,
		StoreBootstrapCred: deploymentsStore.BootstrapCred,

		Uploader:       s3.NewFromConfig(awscfg),
		Edge:           cloudflare.New(),
		Class:          env.GetClass(),
		Lifecycle:      env.GetLifecycle(),
		Identity:       env.GetIdentity(),
		ExpiresAt:      previewExpiry(env.GetLifecycle(), time.Now()),
		RootStackState: priorRootStackState,
		Tag:            req.GetTag(),
	}, manifest, progress, logf)

	// Persist whatever root-stack state was reconciled — even when a later
	// step of this same deploy failed — so the next deploy (and
	// rollback/deployments-ls) reconciles against it instead of starting
	// fresh and orphaning the root stack this run just reconciled. Written to
	// this deploy's own substrate (production or preview), and nil when
	// reconcile itself never ran (an error before it).
	if rootStackState != nil {
		if writeErr := bootstrap.WriteRootStackStateFor(ctx, ssmClient, edgeClass, manifest.GetProjectId(), rootStackState); writeErr != nil {
			if err != nil {
				return outputs, urls, fmt.Errorf("%w (additionally failed to persist root-stack state: %v)", err, writeErr)
			}
			return outputs, urls, writeErr
		}
	}
	return outputs, urls, err
}

// previewTTL is how long an ephemeral preview lives before it is considered
// expired — surfaced by `ocel preview ls` and, later, reaped. Ephemeral
// previews are cheap and recoverable, so a week balances "still there when I
// come back to the PR" against "cleaned up before it leaks".
const previewTTL = 7 * 24 * time.Hour

// previewExpiry returns the epoch-seconds expiry to stamp on a deploy: now +
// previewTTL for an ephemeral preview, 0 (no expiry) for every other lifecycle.
// It is pure.
// readEdgeValues returns whatever the edge provisioned for itself at bootstrap,
// for the deploy path to hand back verbatim. It is the edge's own state: the
// provider persisted it without reading it and passes it on the same way.
//
// Best-effort, like the edge credentials read: a substrate that stored none, or
// one whose policy denies the read, deploys with none rather than failing a
// deploy that otherwise works. The edge then sees no prior state, which is the
// same position it is in on its first deploy.
func readEdgeValues(ctx context.Context, ssmClient bootstrap.SSMAPI, class, bootstrapCmd string, logf func(string)) map[string]string {
	values, err := bootstrap.ReadEdgeValues(ctx, ssmClient, class)
	if err != nil {
		logf("edge bootstrap values unavailable: " + err.Error() + " (re-run `" + bootstrapCmd + "` if the edge needs them)")
		return nil
	}
	return values
}

// cacheStoreUploader addresses the substrate's adopted cache store with the same
// S3 API the origin's cache handler speaks to it. The store is S3-compatible but
// not S3: it carries its own endpoint and its own bucket-scoped credential, so
// it is reached with a static provider rather than the deployer's chain. Nil for
// the zero store — the interface value has to be nil, not a typed nil, for the
// deploy to read it as "no store adopted".
func cacheStoreUploader(store bootstrap.CacheStore) deploy.ArtifactUploader {
	if store.Bucket == "" {
		return nil
	}
	return s3.NewFromConfig(aws.Config{
		Region:      store.Region,
		Credentials: credentials.NewStaticCredentialsProvider(store.AccessKeyID, store.SecretAccessKey, ""),
	}, func(o *s3.Options) {
		o.BaseEndpoint = aws.String(store.Endpoint)
	})
}

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
		return stream.Send(resultEvent(false, err.Error(), nil, nil))
	}
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return stream.Send(resultEvent(false, err.Error(), nil, nil))
	}
	cfn := cloudformation.NewFromConfig(awscfg)
	ssmClient := ssm.NewFromConfig(awscfg)
	iamClient := iam.NewFromConfig(awscfg)

	preview := req.GetClass() == deploymentsv1.Environment_CLASS_PREVIEW

	// The version gate runs on every invocation, bootstrap included. Here a
	// missing or older bootstrap is expected — it's exactly what this call
	// creates or upgrades — so only a bootstrap NEWER than this provider
	// understands is fatal (upgrade the CLI rather than downgrade the account).
	// Each substrate has its own stack, so the gate reads the one being acted on.
	deployed, err := checkBootstrap(ctx, cfn, preview)
	if err != nil {
		return stream.Send(resultEvent(false, err.Error(), nil, nil))
	}
	if bootstrap.CheckCompat(deployed.Version, deployed.Present, bootstrap.RequiredBootstrapVersion) == bootstrap.NeedsCLIUpgrade {
		return stream.Send(resultEvent(false, bootstrap.NeedsCLIUpgrade.Explain().Error(), nil, nil))
	}

	run := bootstrap.Run
	if preview {
		run = bootstrap.RunPreview
	}
	if err := run(ctx, cfn, ssmClient, iamClient, cloudflare.New(), progress, logf); err != nil {
		return stream.Send(resultEvent(false, err.Error(), nil, nil))
	}
	return stream.Send(resultEvent(true, "", nil, nil))
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

// STSAPI is the read subset of the STS client the deploy path uses to resolve
// the account id for the state-table ARN.
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

// phaseProgressEvent builds a phase-tagged ProgressEvent. A non-zero total
// marks a determinate step (current of total); total==0 leaves current/total
// unset so the CLI renders a spinner.
func phaseProgressEvent(phase deploymentsv1.Phase, message string, current, total uint32) *deploymentsv1.DeployEvent {
	p := &deploymentsv1.ProgressEvent{Message: message, Phase: phase}
	if total > 0 {
		p.Current = &current
		p.Total = &total
	}
	return &deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: p},
	}
}

func logEvent(message string) *deploymentsv1.DeployEvent {
	return &deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Log{Log: &deploymentsv1.LogEvent{Message: message}},
	}
}

func resultEvent(success bool, errMsg string, outputs []*deploymentsv1.ResourceOutput, appURLs []string) *deploymentsv1.DeployEvent {
	return &deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{
			Success: success,
			Error:   errMsg,
			Outputs: outputs,
			AppUrls: appURLs,
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
