package server

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
	"time"

	connect "connectrpc.com/connect"

	"github.com/pulumi/pulumi/sdk/v3/go/auto"
	"golang.org/x/sync/errgroup"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/credentials"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	"github.com/aws/aws-sdk-go-v2/service/dynamodb"
	"github.com/aws/aws-sdk-go-v2/service/iam"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/aws/aws-sdk-go-v2/service/s3"
	"github.com/aws/aws-sdk-go-v2/service/secretsmanager"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	"github.com/aws/aws-sdk-go-v2/service/sts"

	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	resourcesv1 "github.com/ocelhq/ocel/pkg/proto/resources/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/pulumiruntime"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/aws/provider/stackindex"
	"github.com/ocelhq/ocel/platform/aws/provider/vars"
	cloudflare "github.com/ocelhq/ocel/platform/edge/cloudflare/deploy"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const deployEnv = "prod"

const pulumiProjectName = "ocel"

type Server struct{}

func stackName(slug string, env *deploymentsv1.Environment) string {
	return slug + "-" + envSegment(env)
}

func envSegment(env *deploymentsv1.Environment) string {
	if env.GetClass() == deploymentsv1.Environment_CLASS_PREVIEW {
		return "preview-" + env.GetIdentity()
	}
	return deployEnv
}

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

func (s *Server) Deploy(ctx context.Context, req *deploymentsv1.DeployRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	manifest := req.GetManifest()
	if err := validateManifest(manifest); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	progress := func(phase deploymentsv1.Phase, m string, current, total uint32) {
		_ = stream.Send(phaseProgressEvent(phase, m, current, total))
	}
	logf := func(m string) { _ = stream.Send(logEvent(m)) }

	res, err := s.runDeploy(ctx, req, manifest, progress, logf)
	if err != nil {
		return stream.Send(failureResult(err))
	}
	return stream.Send(deployedResult(res))
}

func (s *Server) runDeploy(ctx context.Context, req *deploymentsv1.DeployRequest, manifest *deploymentsv1.Manifest, progress deploy.Progress, logf func(string)) (deploy.Result, error) {
	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return deploy.Result{}, err
	}
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return deploy.Result{}, err
	}
	cfn := cloudformation.NewFromConfig(awscfg)
	ssmClient := ssm.NewFromConfig(awscfg)

	env := req.GetEnvironment()
	preview := env.GetClass() == deploymentsv1.Environment_CLASS_PREVIEW
	bootstrapCmd := bootstrapCommand(preview)

	progress(deploymentsv1.Phase_PHASE_UNSPECIFIED, "Checking account bootstrap", 0, 0)
	deployed, err := checkBootstrap(ctx, cfn, preview)
	if err != nil {
		return deploy.Result{}, err
	}
	compat := bootstrap.CheckCompat(deployed.Version, deployed.Present, bootstrap.RequiredBootstrapVersion)
	if err := compat.Explain(deployed.Version, bootstrap.RequiredBootstrapVersion, bootstrapCmd); err != nil {
		return deploy.Result{}, err
	}
	if deployed.StateBucket == "" {
		return deploy.Result{}, fmt.Errorf("account bootstrap is present but its state bucket is missing (a partial rollback?); re-run `%s`", bootstrapCmd)
	}
	if deployed.ArtifactBucket == "" {
		return deploy.Result{}, fmt.Errorf("account bootstrap is present but its artifact bucket is missing (a partial rollback?); re-run `%s`", bootstrapCmd)
	}
	if deployed.AssetBucket == "" {
		return deploy.Result{}, fmt.Errorf("account bootstrap is present but its asset bucket is missing (a partial rollback?); re-run `%s`", bootstrapCmd)
	}
	if deployed.StateTable == "" {
		return deploy.Result{}, fmt.Errorf("account bootstrap is present but its state table is missing (a partial rollback?); re-run `%s`", bootstrapCmd)
	}
	if deployed.VarsTable == "" || deployed.VarsKeyARN == "" {
		return deploy.Result{}, fmt.Errorf("account bootstrap is present but its variable store is missing (a partial rollback?); re-run `%s`", bootstrapCmd)
	}

	substrateClass := bootstrap.ClassProduction
	if preview {
		substrateClass = bootstrap.ClassPreview
	}

	var (
		params         bootstrap.ClassParams
		account        string
		pulumiCmd      auto.PulumiCommand
		varsReferenced map[vars.Coordinate]string
	)
	group, gctx := errgroup.WithContext(ctx)
	group.Go(func() error {
		var err error
		params, err = bootstrap.ReadClassParams(gctx, ssmClient, substrateClass, manifest.GetSlug())
		return err
	})
	group.Go(func() error {
		var err error
		account, err = accountID(gctx, sts.NewFromConfig(awscfg))
		return err
	})
	group.Go(func() error {
		var err error
		varsReferenced, err = referenceOwners(gctx, awscfg, deployed, substrateClass, manifest.GetSlug())
		return err
	})
	group.Go(func() error {
		var err error
		pulumiCmd, err = pulumiruntime.Ensure(gctx, func(m string) {
			progress(deploymentsv1.Phase_PHASE_UPLOADING, m, 0, 0)
		})
		return err
	})
	if err := group.Wait(); err != nil {
		return deploy.Result{}, err
	}

	edgeCreds := params.EdgeCredentials
	if params.EdgeCredentialsErr != nil {
		logf("edge reader credentials unavailable: " + params.EdgeCredentialsErr.Error() +
			" — the worker cannot sign its Function-URL forwards, so every route will 403; re-run `" + bootstrapCmd + "` to mint the edge key")
		edgeCreds = bootstrap.EdgeCredentials{}
	}
	edgeValues := params.EdgeValues
	if params.EdgeValuesErr != nil {
		logf("edge bootstrap values unavailable: " + params.EdgeValuesErr.Error() + " (re-run `" + bootstrapCmd + "` if the edge needs them)")
		edgeValues = nil
	}

	for _, r := range manifest.GetResources() {
		logf(resourceSummary(r))
	}

	priorRootStackState := params.RootStackState
	stateTableARN := fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/%s", awscfg.Region, account, deployed.StateTable)

	res, err := deploy.Run(ctx, deploy.Config{
		Region:        awscfg.Region,
		BackendURL:    deploy.StateBackendURL(deployed.StateBucket, manifest.GetSlug()),
		Passphrase:    params.Passphrase,
		ProjectName:   pulumiProjectName,
		StackName:     stackName(manifest.GetSlug(), env),
		Pulumi:        pulumiCmd,
		Secrets:       secretsmanager.NewFromConfig(awscfg),
		Stacks:        stackIndexFor(awscfg, deployed),
		StateTable:    deployed.StateTable,
		StateTableARN: stateTableARN,

		VarsKeyARN:     deployed.VarsKeyARN,
		VarsTable:      deployed.VarsTable,
		VarsTableARN:   fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/%s", awscfg.Region, account, deployed.VarsTable),
		VarsClass:      substrateClass,
		VarsReferenced: varsReferenced,

		CacheStoreBucket:   params.CacheStore.Bucket,
		CacheStoreUploader: cacheStoreUploader(params.CacheStore),

		ListenerCodePath:   listenerCodePath,
		ArtifactRoot:       artifactRoot(),
		ArtifactBucket:     deployed.ArtifactBucket,
		AssetBucket:        deployed.AssetBucket,
		ImageOptimizerURL:  deployed.ImageOptimizerURL,
		RevalidateQueueURL: deployed.RevalidateQueueURL,
		Env:                envSegment(env),
		EdgeAccessKeyID:    edgeCreds.AccessKeyID,
		EdgeSecretKey:      edgeCreds.SecretAccessKey,
		EdgeValues:         edgeValues,

		Slug:               manifest.GetSlug(),
		StoreScriptName:    params.DeploymentsStore.ScriptName,
		StoreEndpoint:      params.DeploymentsStore.Endpoint,
		StoreBootstrapCred: params.DeploymentsStore.BootstrapCred,

		ISRWriterEndpoint:      params.ISRWriter.Endpoint,
		ISRWriterBootstrapCred: params.ISRWriter.BootstrapCred,
		ISRWriterScriptName:    params.ISRWriter.ScriptName,
		ISRWriterSeed:          params.ISRWriterSeed,

		Uploader:       s3.NewFromConfig(awscfg),
		Invoker:        lambda.NewFromConfig(awscfg),
		Getter:         s3.NewFromConfig(awscfg),
		CodeUpdater:    lambda.NewFromConfig(awscfg),
		Edge:           cloudflare.New(),
		Class:          env.GetClass(),
		Lifecycle:      env.GetLifecycle(),
		Identity:       env.GetIdentity(),
		ExpiresAt:      previewExpiry(env.GetLifecycle(), time.Now()),
		RootStackState: priorRootStackState,
		Tag:            req.GetTag(),
	}, manifest, progress, logf)

	if rootStackStateChanged(priorRootStackState, res.RootStackState) {
		if writeErr := bootstrap.WriteRootStackStateFor(ctx, ssmClient, substrateClass, manifest.GetSlug(), res.RootStackState); writeErr != nil {
			if err != nil {
				return res, fmt.Errorf("%w (additionally failed to persist root-stack state: %v)", err, writeErr)
			}
			return res, writeErr
		}
	}
	return res, err
}

func rootStackStateChanged(prior, reconciled edge.RootStackState) bool {
	return reconciled != nil && !maps.Equal(reconciled, prior)
}

const previewTTL = 7 * 24 * time.Hour

func readEdgeValues(ctx context.Context, ssmClient bootstrap.SSMAPI, class, bootstrapCmd string, logf func(string)) map[string]string {
	values, err := bootstrap.ReadEdgeValues(ctx, ssmClient, class)
	if err != nil {
		logf("edge bootstrap values unavailable: " + err.Error() + " (re-run `" + bootstrapCmd + "` if the edge needs them)")
		return nil
	}
	return values
}

func cacheStoreUploader(store bootstrap.CacheStore) deploy.ArtifactUploader {
	if store.Bucket == "" {
		return nil
	}
	return s3.NewFromConfig(aws.Config{
		Region:      store.Region,
		Credentials: credentials.NewStaticCredentialsProvider(store.AccessKeyID, store.SecretAccessKey, ""),
		Retryer:     sdkconfig.ControlRetryer,
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

func (s *Server) Bootstrap(ctx context.Context, req *deploymentsv1.BootstrapRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	progress := func(m string) { _ = stream.Send(progressEvent(m)) }
	logf := func(m string) { _ = stream.Send(logEvent(m)) }

	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return stream.Send(failureResult(err))
	}
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return stream.Send(failureResult(err))
	}
	cfn := cloudformation.NewFromConfig(awscfg)
	ssmClient := ssm.NewFromConfig(awscfg)
	iamClient := iam.NewFromConfig(awscfg)

	preview := req.GetClass() == deploymentsv1.Environment_CLASS_PREVIEW

	deployed, err := checkBootstrap(ctx, cfn, preview)
	if err != nil {
		return stream.Send(failureResult(err))
	}
	if compat := bootstrap.CheckCompat(deployed.Version, deployed.Present, bootstrap.RequiredBootstrapVersion); compat == bootstrap.NeedsCLIUpgrade {
		return stream.Send(failureResult(compat.Explain(deployed.Version, bootstrap.RequiredBootstrapVersion, bootstrapCommand(preview))))
	}

	run := bootstrap.Run
	if preview {
		run = bootstrap.RunPreview
	}
	artifact := bootstrap.Artifacts{
		Source: bootstrap.ReleaseSource{},
		Store:  s3.NewFromConfig(awscfg),
	}
	if err := run(ctx, cfn, ssmClient, iamClient, cloudflare.New(), artifact, progress, logf); err != nil {
		return stream.Send(failureResult(err))
	}
	return stream.Send(okResult())
}

func bootstrapCommand(preview bool) string {
	if preview {
		return "ocel bootstrap --preview"
	}
	return "ocel bootstrap"
}

func checkBootstrap(ctx context.Context, api bootstrap.CFNDescriber, preview bool) (bootstrap.Deployed, error) {
	if preview {
		return bootstrap.CheckDeployedPreview(ctx, api)
	}
	return bootstrap.CheckDeployed(ctx, api)
}

const listenerCodePathEnvVar = "OCEL_LISTENER_CODE_PATH"

var listenerCodePath = os.Getenv(listenerCodePathEnvVar)

const artifactRootDirName = ".ocel/output"

func artifactRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return artifactRootDirName
	}
	return filepath.Join(wd, artifactRootDirName)
}

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

func loadAWS(ctx context.Context, region string) (aws.Config, error) {
	return sdkconfig.Control(ctx, region)
}

func stackIndexFor(awscfg aws.Config, deployed bootstrap.Deployed) deploy.StackIndex {
	if deployed.StateTable == "" {
		return nil
	}
	return &stackindex.Index{
		Dynamo: dynamodb.NewFromConfig(awscfg),
		Table:  deployed.StateTable,
	}
}

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

func failureResult(err error) *deploymentsv1.DeployEvent {
	return &deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{
			Success: false,
			Error:   err.Error(),
		}},
	}
}

func okResult() *deploymentsv1.DeployEvent {
	return &deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{Success: true}},
	}
}

func deployedResult(res deploy.Result) *deploymentsv1.DeployEvent {
	return &deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Result{Result: &deploymentsv1.ResultEvent{
			Success:     true,
			Outputs:     res.Outputs,
			AppUrls:     res.AppURLs,
			PromotionId: res.PromotionID,
		}},
	}
}

func validateManifest(m *deploymentsv1.Manifest) error {
	if m == nil {
		return fmt.Errorf("manifest is required")
	}
	if m.GetSchemaVersion() == "" {
		return fmt.Errorf("manifest.schema_version is required")
	}
	if m.GetSlug() == "" {
		return fmt.Errorf("manifest.slug is required")
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
