package server

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
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

	"github.com/ocelhq/ocel/pkg/naming"
	environmentv1 "github.com/ocelhq/ocel/pkg/proto/common/environment/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/common/links/v1"
	progressv1 "github.com/ocelhq/ocel/pkg/proto/common/progress/v1"
	contractv1 "github.com/ocelhq/ocel/pkg/proto/provider/contract/v1"
	"github.com/ocelhq/ocel/platform/aws/provider/bootstrap"
	"github.com/ocelhq/ocel/platform/aws/provider/certs"
	"github.com/ocelhq/ocel/platform/aws/provider/deploy"
	"github.com/ocelhq/ocel/platform/aws/provider/dns"
	"github.com/ocelhq/ocel/platform/aws/provider/edges"
	"github.com/ocelhq/ocel/platform/aws/provider/pulumiruntime"
	"github.com/ocelhq/ocel/platform/aws/provider/sdkconfig"
	"github.com/ocelhq/ocel/platform/aws/provider/stackindex"
	"github.com/ocelhq/ocel/platform/aws/provider/transform"
	"github.com/ocelhq/ocel/platform/aws/provider/vars"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const deployEnv = deploy.ProductionEnv

type Server struct {
	stores

	config sessionConfig

	memo memo
}

type memo struct {
	mu       sync.Mutex
	deployed map[string]*entry[bootstrap.Deployed]
	identity map[string]*entry[callerIdentity]

	edgeByOrigin map[string]*entry[edge.Edge]
}

type entry[T any] struct {
	mu     sync.Mutex
	filled bool
	value  T
}

func (e *entry[T]) resolve(fn func() (T, error)) (T, error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	if e.filled {
		return e.value, nil
	}
	v, err := fn()
	if err != nil {
		var zero T
		return zero, err
	}
	e.value, e.filled = v, true
	return v, nil
}

func entryFor[T any](m *memo, table *map[string]*entry[T], key string) *entry[T] {
	m.mu.Lock()
	defer m.mu.Unlock()
	if *table == nil {
		*table = make(map[string]*entry[T])
	}
	e, ok := (*table)[key]
	if !ok {
		e = &entry[T]{}
		(*table)[key] = e
	}
	return e
}

func (m *memo) deployedFor(region string, preview bool) *entry[bootstrap.Deployed] {
	return entryFor(m, &m.deployed, fmt.Sprintf("%s|%t", region, preview))
}

func (m *memo) identityFor(region string) *entry[callerIdentity] {
	return entryFor(m, &m.identity, region)
}

func (m *memo) forgetDeployed() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.deployed = nil
}

func (m *memo) edgeFor(kind edge.Kind, region string) *entry[edge.Edge] {
	return entryFor(m, &m.edgeByOrigin, string(kind)+"|"+region)
}

type edgeSelector interface {
	GetEdge() *contractv1.EdgeSelection
}

func requestedEdge(req edgeSelector) edge.Kind {
	if kind := edge.Kind(req.GetEdge().GetKind()); kind != "" {
		return kind
	}
	return edges.DefaultKind
}

func requestedDNS(req edgeSelector) *contractv1.Dns {
	return req.GetEdge().GetDns()
}

func (s *Server) edge(kind edge.Kind, region string) (edge.Edge, error) {
	return s.memo.edgeFor(kind, region).resolve(func() (edge.Edge, error) {
		return edges.EdgeFor(kind, edges.Deps{
			AWS: func(ctx context.Context) (aws.Config, error) { return loadAWS(ctx, region) },
		})
	})
}

func (s *Server) deployed(ctx context.Context, api bootstrap.CFNDescriber, region string, preview bool) (bootstrap.Deployed, error) {
	return s.memo.deployedFor(region, preview).resolve(func() (bootstrap.Deployed, error) {
		return checkBootstrap(ctx, api, preview)
	})
}

func (s *Server) callerIdentity(ctx context.Context, api STSAPI, region string) (callerIdentity, error) {
	return s.memo.identityFor(region).resolve(func() (callerIdentity, error) {
		return getCallerIdentity(ctx, api)
	})
}

func (s *Server) Deploy(ctx context.Context, req *contractv1.DeployRequest, stream *connect.ServerStream[progressv1.OperationEvent]) (err error) {
	manifest := req.GetManifest()
	if err := validateManifest(manifest); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	edgeFront, edgeErr := s.edge(requestedEdge(req), s.config.get().Region)
	if edgeErr != nil {
		return connect.NewError(connect.CodeInvalidArgument, edgeErr)
	}
	sender := newEventSender(ctx, stream.Send)
	defer func() { err = errors.Join(err, sender.close()) }()

	stages := newDeployStages()
	appStages, appDeclared := deploy.AppStages(stages.provisioning, manifest)
	progress, stageReport, logf, degraded := newDeployReporter(sender, stages)
	tracer := newEventTracer(sender)

	res, deployErr := s.runDeploy(ctx, req, manifest, edgeFront, stages, appStages, appDeclared, progress, stageReport, logf, degraded, tracer)
	if deployErr != nil {
		return sender.fail(deployErr)
	}
	sender.send(deployedResult(res))
	return nil
}

type deployStages struct {
	preparing, uploading, provisioning, finalizing deploy.Stage
}

func newDeployStages() deployStages {
	return deployStages{
		preparing:    deploy.NewRootStage("Preparing"),
		uploading:    deploy.NewRootStage("Uploading"),
		provisioning: deploy.NewRootStage("Provisioning"),
		finalizing:   deploy.NewRootStage("Finalizing"),
	}
}

func (s *Server) runDeploy(ctx context.Context, req *contractv1.DeployRequest, manifest *contractv1.Manifest, edgeFront edge.Edge, stages deployStages, appStages map[string]deploy.Stage, appDeclared []deploy.Stage, progress deploy.Progress, stageReport func(deploy.StageID) func(string), logf func(string), degraded func(edge.Need, string), tracer deploy.Tracer) (deploy.Result, error) {
	if tracer != nil {
		all := append([]deploy.Stage{stages.preparing, stages.uploading, stages.provisioning, stages.finalizing}, appDeclared...)
		tracer.DeclareStages(true, all...)
	}

	preparingStart := time.Now()
	finishPreparing := func(err error) error {
		if tracer != nil {
			tracer.Span(stages.preparing.ID, stages.preparing.ParentID, stages.preparing.Title, preparingStart, time.Now(), err)
		}
		return err
	}

	opts := s.config.get()
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return deploy.Result{}, finishPreparing(err)
	}
	cfn := cloudformation.NewFromConfig(awscfg)
	ssmClient := ssm.NewFromConfig(awscfg)

	env := req.GetEnvironment()
	envName, err := deploy.EnvName(env)
	if err != nil {
		return deploy.Result{}, finishPreparing(connect.NewError(connect.CodeInvalidArgument, err))
	}
	preview := env.GetTier() == environmentv1.Tier_TIER_PREVIEW
	bootstrapCmd := bootstrapCommand(preview)

	progress(progressv1.Phase_PHASE_UNSPECIFIED, "Checking account bootstrap", 0, 0)
	deployed, err := s.deployed(ctx, cfn, opts.Region, preview)
	if err != nil {
		return deploy.Result{}, finishPreparing(err)
	}
	compat := bootstrap.CheckCompat(deployed.Version, deployed.Present, bootstrap.RequiredBootstrapVersion)
	if err := compat.Explain(deployed.Version, bootstrap.RequiredBootstrapVersion, bootstrapCmd); err != nil {
		return deploy.Result{}, finishPreparing(err)
	}
	if err := missingFeatures(deployed, req.GetRequiredFeatures(), preview); err != nil {
		return deploy.Result{}, finishPreparing(err)
	}
	if deployed.StateBucket == "" {
		return deploy.Result{}, finishPreparing(fmt.Errorf("account bootstrap is present but its state bucket is missing (a partial rollback?); re-run `%s`", bootstrapCmd))
	}
	if deployed.ArtifactBucket == "" {
		return deploy.Result{}, finishPreparing(fmt.Errorf("account bootstrap is present but its artifact bucket is missing (a partial rollback?); re-run `%s`", bootstrapCmd))
	}
	if deployed.AssetBucket == "" {
		return deploy.Result{}, finishPreparing(fmt.Errorf("account bootstrap is present but its asset bucket is missing (a partial rollback?); re-run `%s`", bootstrapCmd))
	}
	if deployed.StateTable == "" {
		return deploy.Result{}, finishPreparing(fmt.Errorf("account bootstrap is present but its state table is missing (a partial rollback?); re-run `%s`", bootstrapCmd))
	}
	if deployed.VarsTable == "" || deployed.VarsKeyARN == "" {
		return deploy.Result{}, finishPreparing(fmt.Errorf("account bootstrap is present but its variable store is missing (a partial rollback?); re-run `%s`", bootstrapCmd))
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
		account, err = s.accountID(gctx, sts.NewFromConfig(awscfg), opts.Region)
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
			progress(progressv1.Phase_PHASE_UPLOADING, m, 0, 0)
		})
		return err
	})
	if err := group.Wait(); err != nil {
		return deploy.Result{}, finishPreparing(err)
	}

	admitted, err := admitDomains(ctx, domainGate{
		kind:          edgeFront.Kind(),
		servesUnbound: edgeFront.Facts().ServesUnbound,

		state:     params.Stack.Edge,
		recorded:  params.Stack.Production,
		issuer:    certs.IssuerFor(edgeFront, certs.Deps{AWS: awscfg}),
		prober:    certs.NewProber(),
		pins:      normalizePins(opts.Certificates),
		previewOn: params.PreviewDomain.BaseDomain,
	}, env.GetTier(), manifest, logf)
	if err != nil {
		return deploy.Result{}, finishPreparing(err)
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

	stacks, err := stackIndexFor(awscfg, deployed, bootstrapCmd)
	if err != nil {
		return deploy.Result{}, finishPreparing(err)
	}

	dnsWriter, err := dns.WriterFor(requestedDNS(req).GetKind(), requestedDNS(req).GetZone(), dns.Deps{AWS: awscfg})
	if err != nil {
		return deploy.Result{}, finishPreparing(err)
	}

	priorStackState := params.Stack.Edge
	stateTableARN := fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/%s", awscfg.Region, account, deployed.StateTable)

	finishPreparing(nil)

	res, err := deploy.Run(ctx, deploy.Config{
		Region:           awscfg.Region,
		BackendURL:       naming.StateBackendURL(deployed.StateBucket, manifest.GetSlug()),
		Passphrase:       params.Passphrase,
		PulumiProject:    naming.PulumiProject(manifest.GetSlug()),
		Pulumi:           pulumiCmd,
		Secrets:          secretsmanager.NewFromConfig(awscfg),
		Stacks:           stacks,
		RequiredFeatures: req.GetRequiredFeatures(),
		StateTable:       deployed.StateTable,
		StateTableARN:    stateTableARN,

		VarsKeyARN:         deployed.VarsKeyARN,
		VarsTable:          deployed.VarsTable,
		VarsTableARN:       fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/%s", awscfg.Region, account, deployed.VarsTable),
		VarsClass:          substrateClass,
		VarsSiblingClasses: []string{bootstrap.ClassProduction, bootstrap.ClassPreview},
		VarsReferenced:     varsReferenced,
		Links:              linkStore(awscfg, deployed, substrateClass),

		CacheStoreBucket:   params.CacheStore.Bucket,
		CacheStoreUploader: cacheStoreUploader(params.CacheStore),

		Transform: transformPass(opts),

		ArtifactRoot:       artifactRoot(),
		ArtifactBucket:     deployed.ArtifactBucket,
		AssetBucket:        deployed.AssetBucket,
		ImageOptimizerURL:  deployed.ImageOptimizerURL,
		RevalidateQueueURL: deployed.RevalidateQueueURL,
		Env:                envName,
		EdgeAccessKeyID:    edgeCreds.AccessKeyID,
		EdgeSecretKey:      edgeCreds.SecretAccessKey,
		EdgeValues:         edgeValues,

		GlobalPreviewDomain: params.PreviewDomain.BaseDomain,

		Slug:               manifest.GetSlug(),
		StoreScriptName:    params.DeploymentsStore.ScriptName,
		StoreEndpoint:      params.DeploymentsStore.Endpoint,
		StoreBootstrapCred: params.DeploymentsStore.BootstrapCred,

		ISRWriterEndpoint:      params.ISRWriter.Endpoint,
		ISRWriterBootstrapCred: params.ISRWriter.BootstrapCred,
		ISRWriterScriptName:    params.ISRWriter.ScriptName,
		ISRWriterSeed:          params.ISRWriterSeed,

		OriginSecret: params.OriginSecret,

		Uploader:      s3.NewFromConfig(awscfg),
		Invoker:       lambda.NewFromConfig(awscfg),
		Getter:        s3.NewFromConfig(awscfg),
		CodeUpdater:   lambda.NewFromConfig(awscfg),
		Edge:          edgeFront,
		DNS:           dnsWriter,
		DNSAwait:      dns.NewPoller(),
		AllowDegraded: req.GetEdge().GetAllowDegraded(),
		Degraded:      degraded,
		Tier:          env.GetTier(),
		Lifecycle:     env.GetLifecycle(),
		Identity:      env.GetIdentity(),
		ExpiresAt:     previewExpiry(env.GetLifecycle(), time.Now()),
		StackState:    priorStackState,
		Tag:           req.GetTag(),

		Stages: deploy.Stages{
			Uploading:    stages.uploading,
			Provisioning: stages.provisioning,
			Finalizing:   stages.finalizing,
		},
		AppStages:   appStages,
		Tracer:      tracer,
		StageReport: stageReport,
	}, manifest, progress, logf)

	if err == nil && admitted.withheldURLs != "" {
		res.AppURLs, res.URLNote = nil, admitted.withheldURLs
	}

	if stackStateChanged(priorStackState, res.StackState) {
		record := bootstrap.StackRecord{Edge: res.StackState, Production: params.Stack.Production}
		if writeErr := bootstrap.WriteStackRecordFor(ctx, ssmClient, substrateClass, manifest.GetSlug(), record); writeErr != nil {
			if err != nil {
				return res, fmt.Errorf("%w (additionally failed to persist edge-stack state: %v)", err, writeErr)
			}
			return res, writeErr
		}
	}
	return res, err
}

func stackStateChanged(prior, reconciled edge.StackState) bool {
	return !reconciled.Empty() && !reconciled.Equal(prior)
}

const previewTTL = 7 * 24 * time.Hour

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

func previewExpiry(lifecycle environmentv1.Lifecycle, now time.Time) int64 {
	if lifecycle != environmentv1.Lifecycle_LIFECYCLE_EPHEMERAL {
		return 0
	}
	return now.Add(previewTTL).Unix()
}

func (s *Server) Bootstrap(ctx context.Context, req *contractv1.BootstrapRequest, stream *connect.ServerStream[progressv1.OperationEvent]) error {
	progress := func(m string) { _ = stream.Send(progressEvent(m)) }
	logf := func(m string) { _ = stream.Send(logEvent(m)) }

	opts := s.config.get()
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return failStream(stream, err)
	}
	cfn := cloudformation.NewFromConfig(awscfg)

	preview := req.GetTier() == environmentv1.Tier_TIER_PREVIEW

	deployed, err := checkBootstrap(ctx, cfn, preview)
	if err != nil {
		return failStream(stream, err)
	}
	if compat := bootstrap.CheckCompat(deployed.Version, deployed.Present, bootstrap.RequiredBootstrapVersion); compat == bootstrap.NeedsCLIUpgrade {
		return failStream(stream, compat.Explain(deployed.Version, bootstrap.RequiredBootstrapVersion, bootstrapCommand(preview)))
	}

	defaultEdge, err := s.edge(edges.DefaultKind, opts.Region)
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	apis := bootstrap.APIs{
		CFN:   cfn,
		SSM:   ssm.NewFromConfig(awscfg),
		IAM:   iam.NewFromConfig(awscfg),
		Store: s3.NewFromConfig(awscfg),
		Edge:  defaultEdge,
	}
	if deployed.StateTable != "" {
		apis.Users = &stackindex.Index{Dynamo: dynamodb.NewFromConfig(awscfg), Table: deployed.StateTable}
	}
	request := bootstrap.Request{Features: req.GetFeatures(), Force: req.GetForce()}
	if err := s.runBootstrap(ctx, bootstrapRunner(preview), apis, request, progress, logf); err != nil {
		return failStream(stream, err)
	}
	return stream.Send(okResult())
}

type bootstrapRun func(ctx context.Context, apis bootstrap.APIs, req bootstrap.Request, progress, log func(string)) error

func bootstrapRunner(preview bool) bootstrapRun {
	if preview {
		return bootstrap.RunPreview
	}
	return bootstrap.Run
}

func (s *Server) runBootstrap(ctx context.Context, run bootstrapRun, apis bootstrap.APIs, req bootstrap.Request, progress, logf func(string)) error {
	if err := run(ctx, apis, req, progress, logf); err != nil {
		return err
	}
	s.memo.forgetDeployed()
	return nil
}

func (s *Server) DescribeBootstrap(ctx context.Context, req *contractv1.DescribeBootstrapRequest) (*contractv1.DescribeBootstrapResponse, error) {
	opts := s.config.get()
	awscfg, err := loadAWS(ctx, opts.Region)
	if err != nil {
		return nil, err
	}
	deployed, err := s.deployed(ctx, cloudformation.NewFromConfig(awscfg), opts.Region, req.GetTier() == environmentv1.Tier_TIER_PREVIEW)
	if err != nil {
		return nil, err
	}
	recorded := map[string][]string{}
	if deployed.StateTable != "" {
		index := &stackindex.Index{Dynamo: dynamodb.NewFromConfig(awscfg), Table: deployed.StateTable}
		if recorded, err = index.ProjectFeatures(ctx); err != nil {
			return nil, err
		}
	}

	return describedBootstrap(deployed, recorded), nil
}

func describedBootstrap(deployed bootstrap.Deployed, recorded map[string][]string) *contractv1.DescribeBootstrapResponse {
	resp := &contractv1.DescribeBootstrapResponse{}
	for _, f := range bootstrap.Catalogue() {
		resp.Features = append(resp.Features, &contractv1.Feature{
			Name:       f.Name,
			Summary:    f.Summary,
			DependsOn:  f.DependsOn,
			Enabled:    deployed.Features.Has(f.Name),
			Dependents: bootstrap.ProjectsNeeding(recorded, f.Name),
		})
	}
	return resp
}

func bootstrapCommand(preview bool) string {
	if preview {
		return "ocel bootstrap --preview"
	}
	return "ocel bootstrap"
}

func missingFeatures(deployed bootstrap.Deployed, required []string, preview bool) error {
	missing := deployed.Features.Missing(required)
	if len(missing) == 0 {
		return nil
	}
	full := append(deployed.Features.Names(), missing...)
	slices.Sort(full)
	return fmt.Errorf(
		"this AWS account's Ocel bootstrap lacks the features this project needs: %s.\nRun `%s --features %s` and try again",
		strings.Join(missing, ", "), bootstrapCommand(preview), strings.Join(slices.Compact(full), ","),
	)
}

func checkBootstrap(ctx context.Context, api bootstrap.CFNDescriber, preview bool) (bootstrap.Deployed, error) {
	if preview {
		return bootstrap.CheckDeployedPreview(ctx, api)
	}
	return bootstrap.CheckDeployed(ctx, api)
}

const artifactRootDirName = ".ocel/output"

func transformPass(opts providerConfig) transform.Evaluator {
	if len(opts.Transforms) == 0 {
		return nil
	}
	return transform.NodePass{Root: projectRoot(), Modules: opts.Transforms}
}

func projectRoot() string {
	wd, err := os.Getwd()
	if err != nil {
		return "."
	}
	return wd
}

func artifactRoot() string {
	return filepath.Join(projectRoot(), artifactRootDirName)
}

type STSAPI interface {
	GetCallerIdentity(ctx context.Context, in *sts.GetCallerIdentityInput, optFns ...func(*sts.Options)) (*sts.GetCallerIdentityOutput, error)
}

type callerIdentity struct {
	account string
	arn     string
}

func (id callerIdentity) principal() string {
	if i := strings.LastIndex(id.arn, "/"); i >= 0 {
		return id.arn[i+1:]
	}
	if i := strings.LastIndex(id.arn, ":"); i >= 0 {
		return id.arn[i+1:]
	}
	return id.arn
}

func getCallerIdentity(ctx context.Context, api STSAPI) (callerIdentity, error) {
	out, err := api.GetCallerIdentity(ctx, &sts.GetCallerIdentityInput{})
	if err != nil {
		return callerIdentity{}, err
	}
	return callerIdentity{account: aws.ToString(out.Account), arn: aws.ToString(out.Arn)}, nil
}

func (s *Server) accountID(ctx context.Context, api STSAPI, region string) (string, error) {
	id, err := s.callerIdentity(ctx, api, region)
	if err != nil {
		return "", fmt.Errorf("resolve AWS account id: %w", err)
	}
	return id.account, nil
}

func loadAWS(ctx context.Context, region string) (aws.Config, error) {
	return sdkconfig.Control(ctx, region)
}

func stackIndexFor(awscfg aws.Config, deployed bootstrap.Deployed, bootstrapCmd string) (deploy.StackIndex, error) {
	if deployed.StateTable == "" {
		return nil, fmt.Errorf("account bootstrap is present but its state table is missing (a partial rollback?); re-run `%s`", bootstrapCmd)
	}
	return &stackindex.Index{
		Dynamo: dynamodb.NewFromConfig(awscfg),
		Table:  deployed.StateTable,
	}, nil
}

func resourceSummary(r *contractv1.ManifestResource) string {
	switch cfg := r.GetConfig().(type) {
	case *contractv1.ManifestResource_Postgres:
		return fmt.Sprintf("%s: postgres version=%s", r.GetLogicalName(), cfg.Postgres.GetVersion())
	case *contractv1.ManifestResource_Bucket:
		return fmt.Sprintf("%s: bucket allowed_origins=%v", r.GetLogicalName(), cfg.Bucket.GetAllowedOrigins())
	default:
		return fmt.Sprintf("%s: received config", r.GetLogicalName())
	}
}

func configMatchesType(r *contractv1.ManifestResource, t linksv1.LinkType) bool {
	switch t {
	case linksv1.LinkType_LINK_TYPE_POSTGRES:
		return r.GetPostgres() != nil
	case linksv1.LinkType_LINK_TYPE_BUCKET:
		return r.GetBucket() != nil
	}
	return false
}

func progressEvent(message string) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Progress{Progress: &progressv1.ProgressEvent{Message: message}},
	}
}

func phaseProgressEvent(stageID []byte, phase progressv1.Phase, message string, current, total uint32) *progressv1.OperationEvent {
	p := &progressv1.ProgressEvent{Message: message, Phase: phase, StageId: stageID}
	if total > 0 {
		p.Current = &current
		p.Total = &total
	}
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Progress{Progress: p},
	}
}

func stageProgressEvent(id deploy.StageID, phase progressv1.Phase, message string) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Progress{Progress: &progressv1.ProgressEvent{
			Message: message,
			Phase:   phase,
			StageId: id[:],
		}},
	}
}

func closeStages(tracer deploy.Tracer) {
	deploy.DeclareStages(tracer, true)
}

func degradedEvent(need edge.Need, detail string) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Degraded{Degraded: &progressv1.DegradedEvent{
			Need:   string(need),
			Detail: detail,
		}},
	}
}

func dnsOwedEvent(headline string, records []edge.Record, notes ...string) *progressv1.OperationEvent {
	owed := make([]*progressv1.DnsRecord, 0, len(records))
	for _, rec := range records {
		owed = append(owed, &progressv1.DnsRecord{
			Name:    rec.Name,
			Type:    string(rec.Type),
			Value:   rec.Value,
			Proxied: rec.Proxied,
		})
	}
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_DnsOwed{DnsOwed: &progressv1.DnsOwedEvent{
			Headline: headline,
			Records:  owed,
			Notes:    notes,
		}},
	}
}

func logEvent(message string) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Log{Log: &progressv1.LogEvent{Message: message}},
	}
}

func refusedRequest(err error) bool {
	return connect.CodeOf(err) == connect.CodeInvalidArgument
}

func failStream(stream *connect.ServerStream[progressv1.OperationEvent], err error) error {
	if refusedRequest(err) {
		return err
	}
	return stream.Send(failureResult(err))
}

func failureResult(err error) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Result{Result: &progressv1.ResultEvent{
			Success: false,
			Error:   err.Error(),
		}},
	}
}

func okResult() *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Result{Result: &progressv1.ResultEvent{Success: true}},
	}
}

func deployedResult(res deploy.Result) *progressv1.OperationEvent {
	return &progressv1.OperationEvent{
		Event: &progressv1.OperationEvent_Result{Result: &progressv1.ResultEvent{
			Success:     true,
			Links:       res.Links,
			Functions:   res.Functions,
			AppUrls:     res.AppURLs,
			UrlNote:     res.URLNote,
			PromotionId: res.PromotionID,
			FlipBound:   toFlipBoundProto(res.Flip),
		}},
	}
}

func validateManifest(m *contractv1.Manifest) error {
	for i, r := range m.GetResources() {
		if r.GetLogicalName() == "" {
			return fmt.Errorf("manifest.resources[%d]: logical_name is required", i)
		}
		t := r.GetResource().GetType()
		if _, ok := naming.KindOf(t); !ok {
			return fmt.Errorf("manifest.resources[%d] (%s): a valid resource type is required", i, r.GetLogicalName())
		}
		if r.GetConfig() == nil {
			return fmt.Errorf("manifest.resources[%d] (%s): typed config is required", i, r.GetLogicalName())
		}
		if !configMatchesType(r, t) {
			return fmt.Errorf("manifest.resources[%d] (%s): config does not match resource type %s", i, r.GetLogicalName(), t)
		}
	}
	return nil
}
