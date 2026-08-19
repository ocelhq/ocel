package server

import (
	"context"
	"encoding/json"
	"fmt"
	"maps"
	"os"
	"path/filepath"
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
	deploymentsv1 "github.com/ocelhq/ocel/pkg/proto/deployments/v1"
	linksv1 "github.com/ocelhq/ocel/pkg/proto/links/v1"
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

type edgeNamer interface{ GetEdgeKind() string }

func requestedEdge(req edgeNamer) edge.Kind {
	if kind := edge.Kind(req.GetEdgeKind()); kind != "" {
		return kind
	}
	return edges.DefaultKind
}

func (s *Server) edge(kind edge.Kind, region string) (edge.Edge, error) {
	return s.memo.edgeFor(kind, region).resolve(func() (edge.Edge, error) {
		return edges.EdgeFor(kind, edges.Deps{
			AWS: func(ctx context.Context) (aws.Config, error) { return loadAWS(ctx, region) },
		})
	})
}

func optionsRegion(raw []byte) string {
	opts, err := parseOptions(raw)
	if err != nil {
		return ""
	}
	return opts.Region
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

type options struct {
	Region       string            `json:"region"`
	Transforms   []string          `json:"transforms"`
	Certificates map[string]string `json:"certificates"`
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

func (s *Server) Deploy(ctx context.Context, req *deploymentsv1.DeployRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) (err error) {
	manifest := req.GetManifest()
	if err := validateManifest(manifest); err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}
	edgeFront, edgeErr := s.edge(requestedEdge(req), optionsRegion(req.GetOptions()))
	if edgeErr != nil {
		return connect.NewError(connect.CodeInvalidArgument, edgeErr)
	}
	sender := newEventSender(ctx, stream.Send)
	defer func() { err = sender.close() }()

	stages := newDeployStages()
	appStages, appDeclared := deploy.AppStages(stages.provisioning, manifest)
	progress, stageReport, logf, degraded := newDeployReporter(sender, stages)
	tracer := newEventTracer(sender)

	res, deployErr := s.runDeploy(ctx, req, manifest, edgeFront, stages, appStages, appDeclared, progress, stageReport, logf, degraded, tracer)
	if deployErr != nil {
		sender.send(failureResult(deployErr))
	} else {
		sender.send(deployedResult(res))
	}
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

func (s *Server) runDeploy(ctx context.Context, req *deploymentsv1.DeployRequest, manifest *deploymentsv1.Manifest, edgeFront edge.Edge, stages deployStages, appStages map[string]deploy.Stage, appDeclared []deploy.Stage, progress deploy.Progress, stageReport func(deploy.StageID) func(string), logf func(string), degraded func(edge.Need, string), tracer deploy.Tracer) (deploy.Result, error) {
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

	opts, err := parseOptions(req.GetOptions())
	if err != nil {
		return deploy.Result{}, finishPreparing(err)
	}
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
	preview := env.GetClass() == deploymentsv1.Environment_CLASS_PREVIEW
	bootstrapCmd := bootstrapCommand(preview)

	progress(deploymentsv1.Phase_PHASE_UNSPECIFIED, "Checking account bootstrap", 0, 0)
	deployed, err := s.deployed(ctx, cfn, opts.Region, preview)
	if err != nil {
		return deploy.Result{}, finishPreparing(err)
	}
	compat := bootstrap.CheckCompat(deployed.Version, deployed.Present, bootstrap.RequiredBootstrapVersion)
	if err := compat.Explain(deployed.Version, bootstrap.RequiredBootstrapVersion, bootstrapCmd); err != nil {
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
			progress(deploymentsv1.Phase_PHASE_UPLOADING, m, 0, 0)
		})
		return err
	})
	if err := group.Wait(); err != nil {
		return deploy.Result{}, finishPreparing(err)
	}

	recordedDomains, err := bootstrap.ReadProduction(params.StackState)
	if err != nil {
		return deploy.Result{}, finishPreparing(err)
	}
	admitted, err := admitDomains(ctx, domainGate{
		kind:          edgeFront.Kind(),
		servesUnbound: edge.ServesUnbound(edgeFront),

		state:     params.StackState,
		recorded:  recordedDomains,
		issuer:    certs.IssuerFor(edgeFront, certs.Deps{AWS: awscfg}),
		prober:    certs.NewProber(),
		pins:      normalizePins(opts.Certificates),
		previewOn: params.PreviewDomain.BaseDomain,
	}, env.GetClass(), manifest, logf)
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

	dnsWriter, err := dns.WriterFor(req.GetDns().GetKind(), req.GetDns().GetZone(), dns.Deps{AWS: awscfg})
	if err != nil {
		return deploy.Result{}, finishPreparing(err)
	}

	priorStackState := params.StackState
	stateTableARN := fmt.Sprintf("arn:aws:dynamodb:%s:%s:table/%s", awscfg.Region, account, deployed.StateTable)

	finishPreparing(nil)

	res, err := deploy.Run(ctx, deploy.Config{
		Region:        awscfg.Region,
		BackendURL:    naming.StateBackendURL(deployed.StateBucket, manifest.GetSlug()),
		Passphrase:    params.Passphrase,
		PulumiProject: naming.PulumiProject(manifest.GetSlug()),
		Pulumi:        pulumiCmd,
		Secrets:       secretsmanager.NewFromConfig(awscfg),
		Stacks:        stacks,
		StateTable:    deployed.StateTable,
		StateTableARN: stateTableARN,

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

		ListenerCodePath:   listenerCodePath,
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
		AllowDegraded: req.GetAllowDegraded(),
		Degraded:      degraded,
		Class:         env.GetClass(),
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
		if writeErr := bootstrap.WriteStackStateFor(ctx, ssmClient, substrateClass, manifest.GetSlug(), res.StackState); writeErr != nil {
			if err != nil {
				return res, fmt.Errorf("%w (additionally failed to persist edge-stack state: %v)", err, writeErr)
			}
			return res, writeErr
		}
	}
	return res, err
}

func stackStateChanged(prior, reconciled edge.StackState) bool {
	return reconciled != nil && !maps.Equal(reconciled, prior)
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

func previewExpiry(lifecycle deploymentsv1.Environment_Lifecycle, now time.Time) int64 {
	if lifecycle != deploymentsv1.Environment_LIFECYCLE_EPHEMERAL {
		return 0
	}
	return now.Add(previewTTL).Unix()
}

func (s *Server) Bootstrap(ctx context.Context, req *deploymentsv1.BootstrapRequest, stream *connect.ServerStream[deploymentsv1.DeployEvent]) error {
	edgeFront, err := s.edge(requestedEdge(req), optionsRegion(req.GetOptions()))
	if err != nil {
		return connect.NewError(connect.CodeInvalidArgument, err)
	}

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

	artifact := bootstrap.Artifacts{
		Source: bootstrap.ReleaseSource{},
		Store:  s3.NewFromConfig(awscfg),
	}
	if err := s.runBootstrap(ctx, bootstrapRunner(preview), cfn, ssmClient, iamClient, edgeFront, artifact, progress, logf); err != nil {
		return stream.Send(failureResult(err))
	}
	return stream.Send(okResult())
}

type bootstrapRun func(ctx context.Context, cfn bootstrap.CFNAPI, ssmClient bootstrap.SSMAPI, iamClient bootstrap.IAMAPI, edgeProvider edge.Edge, artifact bootstrap.Artifacts, progress, log func(string)) error

func bootstrapRunner(preview bool) bootstrapRun {
	if preview {
		return bootstrap.RunPreview
	}
	return bootstrap.Run
}

func (s *Server) runBootstrap(ctx context.Context, run bootstrapRun, cfn bootstrap.CFNAPI, ssmClient bootstrap.SSMAPI, iamClient bootstrap.IAMAPI, edgeFront edge.Edge, artifact bootstrap.Artifacts, progress, logf func(string)) error {
	if err := run(ctx, cfn, ssmClient, iamClient, edgeFront, artifact, progress, logf); err != nil {
		return err
	}
	s.memo.forgetDeployed()
	return nil
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

var listenerCodePath = os.Getenv("OCEL_LISTENER_CODE_PATH")

const artifactRootDirName = ".ocel/output"

func transformPass(opts options) transform.Evaluator {
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

func configMatchesType(r *deploymentsv1.ManifestResource, t linksv1.LinkType) bool {
	switch t {
	case linksv1.LinkType_LINK_TYPE_POSTGRES:
		return r.GetPostgres() != nil
	case linksv1.LinkType_LINK_TYPE_BUCKET:
		return r.GetBucket() != nil
	}
	return false
}

func progressEvent(message string) *deploymentsv1.DeployEvent {
	return &deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{Message: message}},
	}
}

func phaseProgressEvent(stageID []byte, phase deploymentsv1.Phase, message string, current, total uint32) *deploymentsv1.DeployEvent {
	p := &deploymentsv1.ProgressEvent{Message: message, Phase: phase, StageId: stageID}
	if total > 0 {
		p.Current = &current
		p.Total = &total
	}
	return &deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: p},
	}
}

func stageProgressEvent(id deploy.StageID, phase deploymentsv1.Phase, message string) *deploymentsv1.DeployEvent {
	return &deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Progress{Progress: &deploymentsv1.ProgressEvent{
			Message: message,
			Phase:   phase,
			StageId: id[:],
		}},
	}
}

func closeStages(tracer deploy.Tracer) {
	deploy.DeclareStages(tracer, true)
}

func degradedEvent(need edge.Need, detail string) *deploymentsv1.DeployEvent {
	return &deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_Degraded{Degraded: &deploymentsv1.DegradedEvent{
			Need:   string(need),
			Detail: detail,
		}},
	}
}

func dnsOwedEvent(headline string, records []edge.Record, notes ...string) *deploymentsv1.DeployEvent {
	owed := make([]*deploymentsv1.DnsRecord, 0, len(records))
	for _, rec := range records {
		owed = append(owed, &deploymentsv1.DnsRecord{
			Name:    rec.Name,
			Type:    string(rec.Type),
			Value:   rec.Value,
			Proxied: rec.Proxied,
		})
	}
	return &deploymentsv1.DeployEvent{
		Event: &deploymentsv1.DeployEvent_DnsOwed{DnsOwed: &deploymentsv1.DnsOwedEvent{
			Headline: headline,
			Records:  owed,
			Notes:    notes,
		}},
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
			Links:       res.Links,
			Functions:   res.Functions,
			AppUrls:     res.AppURLs,
			UrlNote:     res.URLNote,
			PromotionId: res.PromotionID,
			FlipBound:   toFlipBoundProto(res.Flip),
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
