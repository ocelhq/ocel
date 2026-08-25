package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	mathrand "math/rand/v2"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/cloudformation"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	smithy "github.com/aws/smithy-go"
	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	"github.com/ocelhq/ocel/platform/aws/provider/tagclock"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	StackName = "ocel-bootstrap"

	PreviewStackName = "ocel-bootstrap-preview"

	PassphraseParamName = "/ocel/pulumi/passphrase"

	EdgeUserName        = "ocel-edge"
	EdgePreviewUserName = "ocel-edge-preview"

	StateTableIndexName = tagclock.IndexName

	outputStateBucket         = "StateBucketName"
	outputStateTable          = "StateTableName"
	outputStateTableARN       = "StateTableArn"
	outputStateTableStreamARN = "StateTableStreamArn"
	outputArtifactBucket      = "ArtifactBucketName"
	outputAssetBucket         = "AssetBucketName"
	outputAssetBucketARN      = "AssetBucketArn"
	outputInfraClass          = "InfrastructureClass"

	artifactExpirationDays     = 30
	artifactAbortMultipartDays = 7

	stateNoncurrentDays     = 7
	stateAbortMultipartDays = 7
)

const (
	ClassProduction = "production"
	ClassPreview    = "preview"
)

type Deployed struct {
	Present            bool
	Schema             int
	Stacks             []StackStamp
	Features           FeatureSet
	StateBucket        string
	StateTable         string
	ArtifactBucket     string
	AssetBucket        string
	VarsTable          string
	VarsKeyARN         string
	ImageOptimizerURL  string
	RevalidateQueueURL string
	AppBoundaryARN     string
	Class              string
	Outputs            map[string]string
}

type CFNDescriber interface {
	DescribeStacks(ctx context.Context, in *cloudformation.DescribeStacksInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DescribeStacksOutput, error)
}

type CFNAPI interface {
	CFNDescriber
	CreateStack(ctx context.Context, in *cloudformation.CreateStackInput, optFns ...func(*cloudformation.Options)) (*cloudformation.CreateStackOutput, error)
	UpdateStack(ctx context.Context, in *cloudformation.UpdateStackInput, optFns ...func(*cloudformation.Options)) (*cloudformation.UpdateStackOutput, error)
	DeleteStack(ctx context.Context, in *cloudformation.DeleteStackInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DeleteStackOutput, error)
	DescribeStackEvents(ctx context.Context, in *cloudformation.DescribeStackEventsInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DescribeStackEventsOutput, error)
	ListStackResources(ctx context.Context, in *cloudformation.ListStackResourcesInput, optFns ...func(*cloudformation.Options)) (*cloudformation.ListStackResourcesOutput, error)
	CreateChangeSet(ctx context.Context, in *cloudformation.CreateChangeSetInput, optFns ...func(*cloudformation.Options)) (*cloudformation.CreateChangeSetOutput, error)
	DescribeChangeSet(ctx context.Context, in *cloudformation.DescribeChangeSetInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DescribeChangeSetOutput, error)
	ExecuteChangeSet(ctx context.Context, in *cloudformation.ExecuteChangeSetInput, optFns ...func(*cloudformation.Options)) (*cloudformation.ExecuteChangeSetOutput, error)
	DeleteChangeSet(ctx context.Context, in *cloudformation.DeleteChangeSetInput, optFns ...func(*cloudformation.Options)) (*cloudformation.DeleteChangeSetOutput, error)
}

type SSMAPI interface {
	SSMBatchAPI
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
	PutParameter(ctx context.Context, in *ssm.PutParameterInput, optFns ...func(*ssm.Options)) (*ssm.PutParameterOutput, error)
	DeleteParameter(ctx context.Context, in *ssm.DeleteParameterInput, optFns ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error)
}

type APIs struct {
	CFN   CFNAPI
	SSM   SSMAPI
	IAM   IAMAPI
	Store ObjectStore
	Edge  edge.Edge
	Edges providerkit.EdgeRegistry
}

type Request struct {
	Features           []string
	Remove             []string
	Writer             providerkit.Writer
	AcceptReplacements bool
}

func CheckDeployed(ctx context.Context, api CFNDescriber) (Deployed, error) {
	return CheckDeployedFor(ctx, api, ClassProduction)
}

func CheckDeployedPreview(ctx context.Context, api CFNDescriber) (Deployed, error) {
	return CheckDeployedFor(ctx, api, ClassPreview)
}

func CheckDeployedFor(ctx context.Context, api CFNDescriber, class string) (Deployed, error) {
	deployed, _, err := readBootstrap(ctx, api, class)
	return deployed, err
}

type Reading struct {
	Deployed Deployed
	class    string
	refs     stackRefs
}

func (r Reading) Class() string { return r.class }

func Read(ctx context.Context, api CFNDescriber, class string) (Reading, error) {
	deployed, refs, err := readBootstrap(ctx, api, class)
	if err != nil {
		return Reading{}, err
	}
	return Reading{Deployed: deployed, class: class, refs: refs}, nil
}

func FeatureOutputs(ctx context.Context, api CFNDescriber, class, name string) (map[string]string, error) {
	f, ok := featureNamed(name)
	if !ok {
		return nil, fmt.Errorf("bootstrap: no feature named %q", name)
	}
	return stackOutputs(ctx, api, f.stackName(class))
}

func readBootstrap(ctx context.Context, api CFNDescriber, class string) (Deployed, stackRefs, error) {
	coreStack, err := StackNameFor(class)
	if err != nil {
		return Deployed{}, stackRefs{}, err
	}
	core, err := describeStack(ctx, api, coreStack)
	if err != nil {
		return Deployed{}, stackRefs{}, err
	}
	if core == nil || stackUnusable(core.StackStatus) {
		return Deployed{Present: false, Features: FeatureSet{}}, stackRefs{}, nil
	}

	d := Deployed{Present: true, Features: FeatureSet{}, Outputs: outputsOf(core)}
	var refs stackRefs
	if err := absorb(&d, &refs, d.Outputs); err != nil {
		return Deployed{}, stackRefs{}, err
	}
	coreStamp := readStamp(core.Tags)
	d.Schema = coreStamp.Schema

	stamps := make(map[string]Stamp, len(featureRegistry))
	for _, f := range featureRegistry {
		stack, err := describeStack(ctx, api, f.stackName(class))
		if err != nil {
			return Deployed{}, stackRefs{}, err
		}
		if stack == nil || stackUnusable(stack.StackStatus) {
			continue
		}
		d.Features[f.name] = true
		stamps[f.name] = readStamp(stack.Tags)
		out := outputsOf(stack)
		maps.Copy(d.Outputs, out)
		if err := absorb(&d, &refs, out); err != nil {
			return Deployed{}, stackRefs{}, err
		}
	}

	target, err := bootstrapFor(class)
	if err != nil {
		return Deployed{}, stackRefs{}, err
	}
	d.Stacks = append(d.Stacks, StackStamp{
		Name:      target.stackName,
		Present:   true,
		Schema:    coreStamp.Schema,
		Digest:    coreStamp.Digest,
		Intended:  TemplateDigest(target.core()),
		WrittenBy: coreStamp.WrittenBy,
	})
	for _, f := range featureRegistry {
		if !d.Features.Has(f.name) {
			d.Stacks = append(d.Stacks, StackStamp{Name: f.stackName(class), Feature: f.name})
			continue
		}
		stamp := stamps[f.name]
		d.Schema = min(d.Schema, stamp.Schema)
		d.Stacks = append(d.Stacks, StackStamp{
			Name:      f.stackName(class),
			Feature:   f.name,
			Present:   true,
			Schema:    stamp.Schema,
			Digest:    stamp.Digest,
			Intended:  TemplateDigest(f.render(class, d.ArtifactBucket, refs, d.Features).body),
			WrittenBy: stamp.WrittenBy,
		})
	}
	return d, refs, nil
}

func standingFeatures(ctx context.Context, api CFNDescriber, class string) (FeatureSet, error) {
	standing := FeatureSet{}
	for _, f := range featureRegistry {
		stack, err := describeStack(ctx, api, f.stackName(class))
		if err != nil {
			return nil, err
		}
		if stack != nil {
			standing[f.name] = true
		}
	}
	return standing, nil
}

func stackUnusable(status cfntypes.StackStatus) bool {
	switch status {
	case cfntypes.StackStatusCreateFailed, cfntypes.StackStatusRollbackComplete, cfntypes.StackStatusRollbackFailed:
		return true
	default:
		return false
	}
}

func absorb(d *Deployed, refs *stackRefs, out map[string]string) error {
	for key, value := range out {
		switch key {
		case outputStateBucket:
			d.StateBucket = value
		case outputStateTable:
			d.StateTable, refs.stateTable = value, value
		case outputStateTableARN:
			refs.stateTableARN = value
		case outputStateTableStreamARN:
			refs.stateTableStreamARN = value
		case outputArtifactBucket:
			d.ArtifactBucket = value
		case outputAssetBucket:
			d.AssetBucket, refs.assetBucket = value, value
		case outputAssetBucketARN:
			refs.assetBucketARN = value
		case outputVarsTable:
			d.VarsTable = value
		case outputVarsKeyARN:
			d.VarsKeyARN = value
		case outputImageOptimizerURL:
			d.ImageOptimizerURL = value
		case outputImageOptimizerARN:
			refs.imageOptimizerARN = value
		case outputRevalidateQueueURL:
			d.RevalidateQueueURL = value
		case outputRevalidateQueueARN:
			refs.revalidateQueueARN = value
		case outputAppBoundaryARN:
			d.AppBoundaryARN = value
		case outputInfraClass:
			d.Class = value
		}
	}
	return nil
}

func describeStack(ctx context.Context, api CFNDescriber, stackName string) (*cfntypes.Stack, error) {
	out, err := api.DescribeStacks(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stackName)})
	if err != nil {
		if isStackNotFound(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("describe %s stack: %w", stackName, err)
	}
	if len(out.Stacks) == 0 {
		return nil, nil
	}
	return &out.Stacks[0], nil
}

func outputsOf(stack *cfntypes.Stack) map[string]string {
	values := make(map[string]string, len(stack.Outputs))
	for _, o := range stack.Outputs {
		values[aws.ToString(o.OutputKey)] = aws.ToString(o.OutputValue)
	}
	return values
}

func stackOutputs(ctx context.Context, api CFNDescriber, stackName string) (map[string]string, error) {
	stack, err := describeStack(ctx, api, stackName)
	if err != nil || stack == nil {
		return nil, err
	}
	return outputsOf(stack), nil
}

type stackPayloads struct {
	optimizer   payloads.Placement
	publisher   payloads.Placement
	invalidator payloads.Placement
	revalidator payloads.Placement
}

type spec struct {
	class     string
	stackName string
	stackStep string
}

func (s spec) core() string { return coreStackTemplate(s.class) }

func productionBootstrap() spec {
	return spec{
		class:     ClassProduction,
		stackName: StackName,
		stackStep: "Ensuring Pulumi state bucket and state table (CloudFormation)",
	}
}

func previewBootstrap() spec {
	return spec{
		class:     ClassPreview,
		stackName: PreviewStackName,
		stackStep: "Ensuring preview infrastructure (CloudFormation)",
	}
}

func bootstrapFor(class string) (spec, error) {
	switch class {
	case ClassProduction:
		return productionBootstrap(), nil
	case ClassPreview:
		return previewBootstrap(), nil
	default:
		return spec{}, fmt.Errorf("bootstrap: unknown class %q", class)
	}
}

func Run(ctx context.Context, apis APIs, class string, req Request, progress, log func(string)) error {
	target, err := specFor(class)
	if err != nil {
		return err
	}
	return run(ctx, apis, target, req, progress, log)
}

func specFor(class string) (spec, error) {
	switch class {
	case ClassProduction:
		return productionBootstrap(), nil
	case ClassPreview:
		return previewBootstrap(), nil
	default:
		return spec{}, fmt.Errorf("there is no %s bootstrap; a bootstrap is either production or preview", class)
	}
}

func run(ctx context.Context, apis APIs, target spec, req Request, progress, log func(string)) error {
	var reporting sync.Mutex
	report := func(f func(string), msg string) {
		if f == nil {
			return
		}
		reporting.Lock()
		defer reporting.Unlock()
		f(msg)
	}
	progressf := func(msg string) { report(progress, msg) }
	logf := func(msg string) { report(log, msg) }

	requested := req.Features
	levels, err := featureLevels(requested)
	if err != nil {
		return err
	}

	progressf("Ensuring the secret the origin's own front authenticates with (SSM SecureString)")
	if _, err := ensureOriginSecret(ctx, apis.SSM, target.class); err != nil {
		return err
	}

	progressf(target.stackStep)
	namedIAM := []cfntypes.Capability{cfntypes.CapabilityCapabilityNamedIam}
	review := admitReplacements(req.AcceptReplacements, logf)
	coreBody := target.core()
	coreTags := stampTags(Stamp{Schema: RequiredSchema, Digest: TemplateDigest(coreBody), WrittenBy: req.Writer.String()})
	if err := upsertCFNStack(ctx, apis.CFN, target.stackName, coreBody, nil, namedIAM, coreTags, review); err != nil {
		return err
	}
	deployed, refs, err := readBootstrap(ctx, apis.CFN, target.class)
	if err != nil {
		return err
	}
	steps := stepDeps{class: target.class, ssm: apis.SSM, iam: apis.IAM, progress: progressf, log: logf}
	dropped := Removing(req.Features, req.Remove)
	if err := tearDownEdges(ctx, apis, steps, dropped); err != nil {
		return err
	}
	if err := bootstrapEdges(ctx, apis, steps, req.Features, dropped); err != nil {
		return err
	}

	progressf("Ensuring Pulumi passphrase (SSM SecureString)")
	created, err := ensurePassphrase(ctx, apis.SSM)
	if err != nil {
		return err
	}
	if created {
		logf("generated a new Pulumi passphrase")
	} else {
		logf("reused the existing Pulumi passphrase")
	}

	if err := dropFeatures(ctx, apis.CFN, steps, target.class, req.Remove, progressf, logf); err != nil {
		return err
	}

	alongside := FeatureSet{}
	for _, name := range requested {
		alongside[name] = true
	}

	for _, level := range levels {
		progressf(fmt.Sprintf("Applying %s (CloudFormation)", strings.Join(featureStackNames(level, target.class), ", ")))
		produced := make([]map[string]string, len(level))
		group, gctx := errgroup.WithContext(ctx)
		for i, name := range level {
			group.Go(func() error {
				f, _ := featureNamed(name)
				stackName := f.stackName(target.class)
				code, err := f.payloads(gctx, apis.Store, deployed.ArtifactBucket)
				if err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				stack := f.template(featureInputs{
					class:     target.class,
					code:      code,
					refs:      refs,
					alongside: alongside,
				})
				tags := stampTags(Stamp{Schema: RequiredSchema, Digest: TemplateDigest(stack.body), WrittenBy: req.Writer.String()})
				if err := upsertCFNStack(gctx, apis.CFN, stackName, stack.body, stack.params, namedIAM, tags, review); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				if produced[i], err = stackOutputs(gctx, apis.CFN, stackName); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				logf(fmt.Sprintf("applied %s", stackName))
				return nil
			})
		}
		if err := group.Wait(); err != nil {
			return err
		}
		for _, out := range produced {
			if err := absorbRefs(&refs, out); err != nil {
				return err
			}
		}

		for _, name := range level {
			f, _ := featureNamed(name)
			if f.after == nil {
				continue
			}
			if err := f.after(ctx, steps); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}
	}

	return nil
}

func dropFeatures(ctx context.Context, cfn CFNAPI, steps stepDeps, class string, dropOrder []string, progressf, logf func(string)) error {
	if len(dropOrder) == 0 {
		return nil
	}
	progressf(fmt.Sprintf("Removing %s (CloudFormation)", strings.Join(featureStackNames(dropOrder, class), ", ")))
	for _, name := range dropOrder {
		f, _ := featureNamed(name)
		if f.drop == nil {
			continue
		}
		if err := f.drop(ctx, steps); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return deleteFeatureStacks(ctx, cfn, class, dropOrder, logf)
}

func openEdge(apis APIs, kind edge.Kind) (edge.Edge, error) {
	if apis.Edge != nil && apis.Edge.Kind() == kind {
		return apis.Edge, nil
	}
	if apis.Edges == nil {
		return nil, fmt.Errorf("bootstrap: this run holds no edge registry, so it cannot reach the %s edge its features stand on", kind)
	}
	return apis.Edges.Open(kind)
}

func bootstrapEdges(ctx context.Context, apis APIs, d stepDeps, features, dropped []string) error {
	torn := EdgeKindsFor(dropped)
	if !slices.Contains(torn, apis.Edge.Kind()) {
		if err := bootstrapEdge(ctx, d, apis.Edge); err != nil {
			return err
		}
	}
	for _, kind := range EdgeKindsFor(features) {
		if kind == apis.Edge.Kind() || slices.Contains(torn, kind) {
			continue
		}
		front, err := openEdge(apis, kind)
		if err != nil {
			return err
		}
		if err := bootstrapEdge(ctx, d, front); err != nil {
			return err
		}
	}
	return nil
}

func tearDownEdges(ctx context.Context, apis APIs, d stepDeps, dropped []string) error {
	for _, kind := range EdgeKindsFor(dropped) {
		front, err := openEdge(apis, kind)
		if err != nil {
			return err
		}
		if err := tearDownEdge(ctx, d, front); err != nil {
			return err
		}
	}
	return nil
}

func tearDownEdge(ctx context.Context, d stepDeps, front edge.Edge) error {
	d.progress(fmt.Sprintf("Tearing the %s edge down", front.Kind()))
	if err := front.Teardown(ctx, edge.Class(d.class)); err != nil {
		return fmt.Errorf("tear down %s edge: %w", front.Kind(), err)
	}
	return nil
}

func bootstrapEdge(ctx context.Context, d stepDeps, front edge.Edge) error {
	d.progress(fmt.Sprintf("Bootstrapping the %s edge", front.Kind()))
	out, err := front.Bootstrap(ctx, edge.Class(d.class))
	if err != nil {
		return fmt.Errorf("bootstrap %s edge: %w", front.Kind(), err)
	}
	for _, offer := range out.Offers {
		switch offer.Kind {
		case edge.OfferCacheStore:
			d.progress("Adopting the edge cache store (SSM SecureString)")
			if err := adoptCacheStore(ctx, d.ssm, d.class, front.Kind(), offer.Values); err != nil {
				return err
			}
		case edge.OfferDeploymentsStore:
			d.progress("Adopting the deployments-store worker (SSM SecureString)")
			if err := adoptDeploymentsStore(ctx, d.ssm, d.class, front.Kind(), offer.Values); err != nil {
				return err
			}
		case edge.OfferISRWriter:
			d.progress("Adopting the ISR writer worker (SSM SecureString)")
			if err := adoptISRWriter(ctx, d.ssm, d.class, front.Kind(), offer.Values); err != nil {
				return err
			}
			if _, err := ensureISRWriterSeed(ctx, d.ssm, d.class, front.Kind()); err != nil {
				return err
			}
		default:
			d.log(fmt.Sprintf("ignoring edge offer %q: no provider resource adopts it", offer.Kind))
		}
	}
	if len(out.Values) == 0 {
		return nil
	}
	d.progress("Storing edge bootstrap outputs (SSM SecureString)")
	return writeEdgeValues(ctx, d.ssm, d.class, front.Kind(), out.Values)
}

func FeatureStackName(name, class string) string {
	f, ok := featureNamed(name)
	if !ok {
		return name
	}
	return f.stackName(class)
}

func absorbRefs(refs *stackRefs, out map[string]string) error {
	var ignored Deployed
	return absorb(&ignored, refs, out)
}

type changeReview func(stackName string, changes []cfntypes.ResourceChange) error

func upsertCFNStack(ctx context.Context, cfn CFNAPI, stackName, template string, params []cfntypes.Parameter, capabilities []cfntypes.Capability, tags []cfntypes.Tag, review changeReview) error {
	stack, err := describeStack(ctx, cfn, stackName)
	if err != nil {
		return err
	}
	if stack != nil && stackUnusable(stack.StackStatus) {
		if err := deleteCFNStack(ctx, cfn, stackName); err != nil {
			return err
		}
		stack = nil
	}
	if stack == nil {
		return createCFNStack(ctx, cfn, stackName, template, params, capabilities, tags)
	}
	return updateCFNStack(ctx, cfn, stackName, template, params, capabilities, tags, review)
}

func updateCFNStack(ctx context.Context, cfn CFNAPI, stackName, template string, params []cfntypes.Parameter, capabilities []cfntypes.Capability, tags []cfntypes.Tag, review changeReview) error {
	id, changes, err := planCFNStack(ctx, cfn, stackName, template, params, capabilities, tags)
	if err != nil {
		return err
	}
	if id == "" {
		return restampCFNStack(ctx, cfn, stackName, params, capabilities, tags)
	}

	executed := false
	defer func() {
		if !executed {
			discardChangeSet(ctx, cfn, id)
		}
	}()

	if review != nil {
		if err := review(stackName, changes); err != nil {
			return err
		}
	}
	if _, err := cfn.ExecuteChangeSet(ctx, &cloudformation.ExecuteChangeSetInput{ChangeSetName: aws.String(id)}); err != nil {
		return fmt.Errorf("update %s stack: %w", stackName, err)
	}
	executed = true

	w := cloudformation.NewStackUpdateCompleteWaiter(cfn)
	if err := w.Wait(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stackName)}, stackWaitTimeout); err != nil {
		return fmt.Errorf("wait for %s update: %w", stackName, err)
	}
	return nil
}

func restampCFNStack(ctx context.Context, cfn CFNAPI, stackName string, params []cfntypes.Parameter, capabilities []cfntypes.Capability, tags []cfntypes.Tag) error {
	stack, err := describeStack(ctx, cfn, stackName)
	if err != nil || stack == nil {
		return err
	}
	if sameStackTags(stack.Tags, tags) {
		return nil
	}
	if _, err := cfn.UpdateStack(ctx, &cloudformation.UpdateStackInput{
		StackName:           aws.String(stackName),
		UsePreviousTemplate: aws.Bool(true),
		Parameters:          params,
		Capabilities:        capabilities,
		Tags:                tags,
	}); err != nil {
		if changeSetEmpty(err.Error()) {
			return nil
		}
		return fmt.Errorf("restamp %s: %w", stackName, err)
	}
	w := cloudformation.NewStackUpdateCompleteWaiter(cfn)
	if err := w.Wait(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stackName)}, stackWaitTimeout); err != nil {
		return fmt.Errorf("wait for the %s restamp: %w", stackName, err)
	}
	return nil
}

func sameStackTags(have, want []cfntypes.Tag) bool {
	if len(have) != len(want) {
		return false
	}
	standing := make(map[string]string, len(have))
	for _, tag := range have {
		standing[aws.ToString(tag.Key)] = aws.ToString(tag.Value)
	}
	for _, tag := range want {
		value, ok := standing[aws.ToString(tag.Key)]
		if !ok || value != aws.ToString(tag.Value) {
			return false
		}
	}
	return true
}

const (
	changeSetAttempts = 40
	changeSetBase     = 2 * time.Second
	changeSetCeiling  = 15 * time.Second
	changeSetJitter   = 0.2
)

func planCFNStack(ctx context.Context, cfn CFNAPI, stackName, template string, params []cfntypes.Parameter, capabilities []cfntypes.Capability, tags []cfntypes.Tag) (string, []cfntypes.ResourceChange, error) {
	out, err := cfn.CreateChangeSet(ctx, &cloudformation.CreateChangeSetInput{
		StackName:     aws.String(stackName),
		ChangeSetName: aws.String(changeSetName()),
		ChangeSetType: cfntypes.ChangeSetTypeUpdate,
		TemplateBody:  aws.String(template),
		Parameters:    params,
		Capabilities:  capabilities,
		Tags:          tags,
	})
	if err != nil {
		return "", nil, fmt.Errorf("plan the %s update: %w", stackName, err)
	}
	id := aws.ToString(out.Id)

	changes, reason, err := awaitChangeSet(ctx, cfn, id)
	if err != nil {
		discardChangeSet(ctx, cfn, id)
		return "", nil, fmt.Errorf("plan the %s update: %w", stackName, err)
	}
	if reason != "" {
		discardChangeSet(ctx, cfn, id)
		if changeSetEmpty(reason) {
			return "", nil, nil
		}
		return "", nil, fmt.Errorf("plan the %s update: %s", stackName, reason)
	}
	return id, changes, nil
}

func awaitChangeSet(ctx context.Context, cfn CFNAPI, id string) ([]cfntypes.ResourceChange, string, error) {
	for attempt := 0; ; attempt++ {
		var changes []cfntypes.ResourceChange
		var token *string
		for {
			out, err := cfn.DescribeChangeSet(ctx, &cloudformation.DescribeChangeSetInput{
				ChangeSetName: aws.String(id),
				NextToken:     token,
			})
			if err != nil {
				return nil, "", err
			}
			for _, c := range out.Changes {
				if c.ResourceChange != nil {
					changes = append(changes, *c.ResourceChange)
				}
			}
			if out.NextToken == nil {
				switch out.Status {
				case cfntypes.ChangeSetStatusFailed:
					return nil, changeSetReason(out), nil
				case cfntypes.ChangeSetStatusCreateComplete:
					return changes, "", nil
				case cfntypes.ChangeSetStatusDeletePending,
					cfntypes.ChangeSetStatusDeleteInProgress,
					cfntypes.ChangeSetStatusDeleteComplete,
					cfntypes.ChangeSetStatusDeleteFailed:
					return nil, "", fmt.Errorf("change set %s is %s: something else took it away while this run was planning against it", id, out.Status)
				}
				break
			}
			token = out.NextToken
		}
		if attempt+1 >= changeSetAttempts {
			return nil, "", fmt.Errorf("change set %s was still being built after %d looks", id, changeSetAttempts)
		}
		if err := holdBefore(ctx, changeSetDelay(attempt)); err != nil {
			return nil, "", err
		}
	}
}

func changeSetReason(out *cloudformation.DescribeChangeSetOutput) string {
	if reason := aws.ToString(out.StatusReason); reason != "" {
		return reason
	}
	return string(cfntypes.ChangeSetStatusFailed)
}

func changeSetEmpty(reason string) bool {
	return strings.Contains(reason, "didn't contain changes") ||
		strings.Contains(reason, "No updates are to be performed")
}

func changeSetDelay(attempt int) time.Duration {
	step := min(changeSetBase<<min(attempt, 3), changeSetCeiling)
	return step + time.Duration(mathrand.Float64()*changeSetJitter*float64(step))
}

func changeSetName() string {
	return fmt.Sprintf("ocel-%d", time.Now().UnixNano())
}

const discardGrace = 20 * time.Second

func discardChangeSet(ctx context.Context, cfn CFNAPI, id string) {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), discardGrace)
	defer cancel()
	_, _ = cfn.DeleteChangeSet(ctx, &cloudformation.DeleteChangeSetInput{ChangeSetName: aws.String(id)})
}

const (
	nameHeldAttempts = 4
	nameHeldBase     = 20 * time.Second
	nameHeldCeiling  = 90 * time.Second
	nameHeldJitter   = 0.2

	heldQueueName = "QueueDeletedRecently"
)

var holdBefore = func(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func createCFNStack(ctx context.Context, cfn CFNAPI, stackName, template string, params []cfntypes.Parameter, capabilities []cfntypes.Capability, tags []cfntypes.Tag) error {
	for attempt := 0; ; attempt++ {
		err := createOnce(ctx, cfn, stackName, template, params, capabilities, tags)
		if err == nil {
			return nil
		}
		if attempt+1 >= nameHeldAttempts || !nameStillHeld(ctx, cfn, stackName) {
			return err
		}
		if err := deleteCFNStack(ctx, cfn, stackName); err != nil {
			return err
		}
		if err := holdBefore(ctx, nameHeldDelay(attempt)); err != nil {
			return err
		}
	}
}

func createOnce(ctx context.Context, cfn CFNAPI, stackName, template string, params []cfntypes.Parameter, capabilities []cfntypes.Capability, tags []cfntypes.Tag) error {
	if _, err := cfn.CreateStack(ctx, &cloudformation.CreateStackInput{
		StackName:    aws.String(stackName),
		TemplateBody: aws.String(template),
		Parameters:   params,
		Capabilities: capabilities,
		Tags:         tags,
	}); err != nil {
		return fmt.Errorf("create %s stack: %w", stackName, err)
	}
	w := cloudformation.NewStackCreateCompleteWaiter(cfn)
	if err := w.Wait(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stackName)}, stackWaitTimeout); err != nil {
		return fmt.Errorf("wait for %s create: %w", stackName, err)
	}
	return nil
}

func nameHeldDelay(attempt int) time.Duration {
	step := min(nameHeldBase<<attempt, nameHeldCeiling)
	return step + time.Duration(mathrand.Float64()*nameHeldJitter*float64(step))
}

func nameStillHeld(ctx context.Context, cfn CFNAPI, stackName string) bool {
	out, err := cfn.DescribeStackEvents(ctx, &cloudformation.DescribeStackEventsInput{StackName: aws.String(stackName)})
	if err != nil {
		return false
	}
	for _, event := range out.StackEvents {
		if strings.Contains(aws.ToString(event.ResourceStatusReason), heldQueueName) {
			return true
		}
	}
	return false
}

func ensurePassphrase(ctx context.Context, ssmClient SSMAPI) (created bool, err error) {
	_, err = ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(PassphraseParamName),
		WithDecryption: aws.Bool(true),
	})
	if err == nil {
		return false, nil
	}
	var notFound *ssmtypes.ParameterNotFound
	if !errors.As(err, &notFound) {
		return false, fmt.Errorf("read passphrase parameter: %w", err)
	}

	passphrase, err := generatePassphrase()
	if err != nil {
		return false, err
	}
	if _, err := ssmClient.PutParameter(ctx, &ssm.PutParameterInput{
		Name:        aws.String(PassphraseParamName),
		Description: aws.String("Ocel: the passphrase every Pulumi stack in this account is encrypted under, production and preview alike. This is the only copy - delete it and that state can never be decrypted again."),
		Value:       aws.String(passphrase),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(false),
	}); err != nil {
		return false, fmt.Errorf("write passphrase parameter: %w", err)
	}
	return true, nil
}

func ReadPassphrase(ctx context.Context, ssmClient SSMAPI) (string, error) {
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(PassphraseParamName),
		WithDecryption: aws.Bool(true),
	})
	if err != nil {
		return "", fmt.Errorf("read passphrase parameter: %w", err)
	}
	return aws.ToString(out.Parameter.Value), nil
}

func generatePassphrase() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate passphrase: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func coreStackTemplate(class string) string {
	return fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: %q
Resources:
%s%s%s%s%s%s%sOutputs:
  %s:
    Description: "S3 bucket holding the Pulumi state Ocel plans every %s deploy and teardown from. One versioned object per %s stack."
    Value: !Ref StateBucket
%s%s%s%s%s  %s:
    Description: "Class this bootstrap is stamped with, checked before an action runs so a preview deploy cannot reach production."
    Value: '%s'
`, coreStackDescription(class),
		stateBucketResource(class), stateTableResource(), artifactBucketResource(), assetBucketResource(), assetBucketPolicyResource(), varsResources(class), appBoundaryResource(class),
		outputStateBucket, class, scopeOf(class),
		stateTableOutputs(), artifactBucketOutput(), assetBucketOutputs(), varsOutputs(), appBoundaryOutput(), outputInfraClass, class)
}

func coreStackDescription(class string) string {
	apart := ""
	if class == ClassPreview {
		apart = " It is kept apart from the production bootstrap so a per-PR preview never reaches production state, variables or caches."
	}
	return fmt.Sprintf("Ocel bootstrap (%s) - the account-global core every %s Ocel deploys here is built on: the Pulumi state bucket and state table, the artifact and asset buckets and the variable store. Each edge this account fronts deployments with stands in a feature stack of its own beside it.%s", class, scopeOf(class), apart)
}

func scopeOf(class string) string {
	if class == ClassPreview {
		return "preview environment"
	}
	return "production app"
}

func stateBucketResource(class string) string {
	return fmt.Sprintf(`  StateBucket:
    Type: AWS::S3::Bucket
    Metadata:
      Description: "Pulumi state for every %s Ocel has deployed here, one versioned object per stack, read to plan a deploy and to tear one down. Emptying it strands what is already deployed."
    Properties:
      BucketEncryption:
        ServerSideEncryptionConfiguration:
          - ServerSideEncryptionByDefault:
              SSEAlgorithm: AES256
      VersioningConfiguration:
        Status: Enabled
      PublicAccessBlockConfiguration:
        BlockPublicAcls: true
        BlockPublicPolicy: true
        IgnorePublicAcls: true
        RestrictPublicBuckets: true
      LifecycleConfiguration:
        Rules:
          - Id: expire-noncurrent-state
            Status: Enabled
            NoncurrentVersionExpiration:
              NoncurrentDays: %d
            AbortIncompleteMultipartUpload:
              DaysAfterInitiation: %d
          - Id: expire-state-delete-markers
            Status: Enabled
            ExpiredObjectDeleteMarker: true
`, scopeOf(class), stateNoncurrentDays, stateAbortMultipartDays)
}

func stateTableResource() string {
	return fmt.Sprintf(`  StateTable:
    Type: AWS::DynamoDB::Table
    Metadata:
      Description: "Account-global Ocel state, keyed by pk/sk: the index of every stack this bootstrap has deployed, which prune and teardown walk, and the tag clock the edge reads and updates."
    Properties:
      BillingMode: PAY_PER_REQUEST
      AttributeDefinitions:
        - AttributeName: pk
          AttributeType: S
        - AttributeName: sk
          AttributeType: S
        - AttributeName: gsi1pk
          AttributeType: S
        - AttributeName: gsi1sk
          AttributeType: S
      KeySchema:
        - AttributeName: pk
          KeyType: HASH
        - AttributeName: sk
          KeyType: RANGE
      GlobalSecondaryIndexes:
        - IndexName: %s
          KeySchema:
            - AttributeName: gsi1pk
              KeyType: HASH
            - AttributeName: gsi1sk
              KeyType: RANGE
          Projection:
            ProjectionType: INCLUDE
            NonKeyAttributes:
              - expired
              - stale
              - tag
      TimeToLiveSpecification:
        AttributeName: expires_at
        Enabled: true
      StreamSpecification:
        StreamViewType: NEW_IMAGE
`, StateTableIndexName)
}

func stateTableOutputs() string {
	return fmt.Sprintf(`  %s:
    Description: "DynamoDB table holding account-global Ocel state: the stack index prune and teardown walk, and the ISR tag clock the edge shares."
    Value: !Ref StateTable
  %s:
    Description: "ARN of that table, handed to every feature stack that grants an item read or write on it."
    Value: !GetAtt StateTable.Arn
  %s:
    Description: "ARN of that table's stream, handed to every feature stack that reads tag writes off it."
    Value: !GetAtt StateTable.StreamArn
`, outputStateTable, outputStateTableARN, outputStateTableStreamARN)
}

func artifactBucketResource() string {
	return fmt.Sprintf(`  ArtifactBucket:
    Type: AWS::S3::Bucket
    Metadata:
      Description: "Staging area for the Lambda code Ocel uploads before a stack references it. A deployed function holds its own copy, and these objects age out on a lifecycle rule."
    Properties:
      BucketEncryption:
        ServerSideEncryptionConfiguration:
          - ServerSideEncryptionByDefault:
              SSEAlgorithm: AES256
      PublicAccessBlockConfiguration:
        BlockPublicAcls: true
        BlockPublicPolicy: true
        IgnorePublicAcls: true
        RestrictPublicBuckets: true
      LifecycleConfiguration:
        Rules:
          - Id: expire-artifacts
            Status: Enabled
            ExpirationInDays: %d
            AbortIncompleteMultipartUpload:
              DaysAfterInitiation: %d
`, artifactExpirationDays, artifactAbortMultipartDays)
}

func artifactBucketOutput() string {
	return fmt.Sprintf(`  %s:
    Description: "S3 bucket Ocel stages function code in before a stack references it. Objects age out on a lifecycle rule."
    Value: !Ref ArtifactBucket
`, outputArtifactBucket)
}

func assetBucketResource() string {
	return fmt.Sprintf(`  AssetBucket:
    Type: AWS::S3::Bucket
    Metadata:
      Description: "Per-build static assets, prerender fallbacks, image-optimizer config and the edge's fetch cache, keyed by build id. Read directly by the edge, the optimizer, the publisher and the revalidator."
    Properties:
      BucketEncryption:
        ServerSideEncryptionConfiguration:
          - ServerSideEncryptionByDefault:
              SSEAlgorithm: AES256
      PublicAccessBlockConfiguration:
        BlockPublicAcls: true
        BlockPublicPolicy: true
        IgnorePublicAcls: true
        RestrictPublicBuckets: true
      LifecycleConfiguration:
        Rules:
          - Id: abort-incomplete-uploads
            Status: Enabled
            AbortIncompleteMultipartUpload:
              DaysAfterInitiation: %d
`, artifactAbortMultipartDays)
}

func assetBucketPolicyResource() string {
	return `  AssetBucketPolicy:
    Type: AWS::S3::BucketPolicy
    Metadata:
      Description: "The only grant on the asset bucket: CloudFront distributions in this account, and no one else, read objects out of it over an origin access control. One statement for the whole account, so no deploy rewrites it."
    Properties:
      Bucket: !Ref AssetBucket
      PolicyDocument:
        Version: '2012-10-17'
        Statement:
          - Sid: AllowCloudFrontOriginAccessControlRead
            Effect: Allow
            Principal:
              Service: cloudfront.amazonaws.com
            Action: s3:GetObject
            Resource: !Sub '${AssetBucket.Arn}/*'
            Condition:
              StringEquals:
                AWS:SourceAccount: !Ref AWS::AccountId
`
}

func assetBucketOutputs() string {
	return fmt.Sprintf(`  %s:
    Description: "S3 bucket holding per-build static assets, prerender fallbacks, image-optimizer config and the edge fetch cache. Read directly by the edge."
    Value: !Ref AssetBucket
  %s:
    Description: "ARN of that bucket, handed to every feature stack that grants a read or a write inside it."
    Value: !GetAtt AssetBucket.Arn
`, outputAssetBucket, outputAssetBucketARN)
}

func isStackNotFound(err error) bool {
	return isValidationErrorContaining(err, "does not exist")
}

func isValidationErrorContaining(err error, substr string) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.ErrorCode() == "ValidationError" && strings.Contains(apiErr.ErrorMessage(), substr)
}

const stackWaitTimeout = 10 * time.Minute
