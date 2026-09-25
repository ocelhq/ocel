package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	"maps"
	"slices"
	"strings"
	"sync"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/providerkit"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	"github.com/ocelhq/ocel/platform/aws/provider/tagclock"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
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
	RuntimeLayers      map[string]string
	RuntimeStack       StackStamp
	Outputs            map[string]string
}

type SSMAPI interface {
	SSMBatchAPI
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
	PutParameter(ctx context.Context, in *ssm.PutParameterInput, optFns ...func(*ssm.Options)) (*ssm.PutParameterOutput, error)
	DeleteParameter(ctx context.Context, in *ssm.DeleteParameterInput, optFns ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error)
}

type APIs struct {
	CFN   cfn.API
	SSM   SSMAPI
	IAM   IAMAPI
	Store ObjectStore
	Edge  edge.Edge
	Edges providerkit.Edges
}

type Request struct {
	VarsKey            string
	Features           []string
	Remove             []string
	Writer             providerkit.WrittenBy
	AcceptReplacements bool
}

func CheckDeployed(ctx context.Context, api cfn.StacksAPI, ns Namespace) (Deployed, error) {
	return CheckDeployedFor(ctx, api, ns, ClassProduction)
}

func CheckDeployedPreview(ctx context.Context, api cfn.StacksAPI, ns Namespace) (Deployed, error) {
	return CheckDeployedFor(ctx, api, ns, ClassPreview)
}

func CheckDeployedFor(ctx context.Context, api cfn.StacksAPI, ns Namespace, class string) (Deployed, error) {
	deployed, _, err := readBootstrap(ctx, api, ns, class)
	return deployed, err
}

type Reading struct {
	Deployed Deployed
	ns       Namespace
	class    string
	refs     stackRefs
}

func (r Reading) Class() string { return r.class }

func (r Reading) Namespace() Namespace { return r.ns }

func Read(ctx context.Context, api cfn.StacksAPI, ns Namespace, class string) (Reading, error) {
	deployed, refs, err := readBootstrap(ctx, api, ns, class)
	if err != nil {
		return Reading{}, err
	}
	return Reading{Deployed: deployed, ns: ns, class: class, refs: refs}, nil
}

func FeatureOutputs(ctx context.Context, api cfn.StacksAPI, ns Namespace, class, name string) (map[string]string, error) {
	f, ok := featureNamed(name)
	if !ok {
		return nil, fmt.Errorf("bootstrap: no feature named %q", name)
	}
	return cfn.StackOutputs(ctx, api, f.stackName(ns, class))
}

func readBootstrap(ctx context.Context, api cfn.StacksAPI, ns Namespace, class string) (Deployed, stackRefs, error) {
	coreStack, err := ns.StackNameFor(class)
	if err != nil {
		return Deployed{}, stackRefs{}, err
	}
	core, err := cfn.DescribeStack(ctx, api, coreStack)
	if err != nil {
		return Deployed{}, stackRefs{}, err
	}
	if core == nil || cfn.Unusable(core.StackStatus) {
		return Deployed{Present: false, Features: FeatureSet{}}, stackRefs{}, nil
	}

	d := Deployed{Present: true, Features: FeatureSet{}, RuntimeLayers: map[string]string{}, Outputs: cfn.OutputsOf(core)}
	var refs stackRefs
	if err := absorb(&d, &refs, d.Outputs); err != nil {
		return Deployed{}, stackRefs{}, err
	}
	coreStamp := readStamp(core.Tags)
	d.Schema = coreStamp.Schema

	if err := readRuntimeLayers(ctx, api, &d, &refs, ns, class); err != nil {
		return Deployed{}, stackRefs{}, err
	}

	stamps := make(map[string]Stamp, len(featureRegistry))
	for _, f := range featureRegistry {
		stack, err := cfn.DescribeStack(ctx, api, f.stackName(ns, class))
		if err != nil {
			return Deployed{}, stackRefs{}, err
		}
		if stack == nil || cfn.Unusable(stack.StackStatus) {
			continue
		}
		d.Features[f.name] = true
		stamps[f.name] = readStamp(stack.Tags)
		out := cfn.OutputsOf(stack)
		maps.Copy(d.Outputs, out)
		if err := absorb(&d, &refs, out); err != nil {
			return Deployed{}, stackRefs{}, err
		}
	}

	target, err := bootstrapFor(ns, class)
	if err != nil {
		return Deployed{}, stackRefs{}, err
	}
	d.Stacks = append(d.Stacks, StackStamp{
		Name:      target.stackName,
		Present:   true,
		Schema:    coreStamp.Schema,
		Digest:    coreStamp.Digest,
		Intended:  cfn.TemplateDigest(target.core(broughtVarsKey(d.Outputs))),
		WrittenBy: coreStamp.WrittenBy,
	})
	for _, f := range featureRegistry {
		if !d.Features.Has(f.name) {
			d.Stacks = append(d.Stacks, StackStamp{Name: f.stackName(ns, class), Feature: f.name})
			continue
		}
		stamp := stamps[f.name]
		d.Schema = min(d.Schema, stamp.Schema)
		d.Stacks = append(d.Stacks, StackStamp{
			Name:    f.stackName(ns, class),
			Feature: f.name,
			Present: true,
			Schema:  stamp.Schema,
			Digest:  stamp.Digest,
			Intended: cfn.TemplateDigest(f.planned(featureInputs{
				ns:             ns,
				class:          class,
				artifactBucket: d.ArtifactBucket,
				refs:           refs,
				alongside:      d.Features,
				varsKey:        broughtVarsKey(d.Outputs),
			}).body),
			WrittenBy: stamp.WrittenBy,
		})
	}
	return d, refs, nil
}

func readRuntimeLayers(ctx context.Context, api cfn.StacksAPI, d *Deployed, refs *stackRefs, ns Namespace, class string) error {
	intended, err := runtimeLayerTemplateAt(ns, class, d.ArtifactBucket)
	if err != nil {
		return err
	}
	stackName := ns.runtimeStackName(class)
	d.RuntimeStack = StackStamp{Name: stackName, Intended: cfn.TemplateDigest(intended)}
	stack, err := cfn.DescribeStack(ctx, api, stackName)
	if err != nil || stack == nil || cfn.Unusable(stack.StackStatus) {
		return err
	}
	out := cfn.OutputsOf(stack)
	maps.Copy(d.Outputs, out)
	if err := absorb(d, refs, out); err != nil {
		return err
	}
	stamp := readStamp(stack.Tags)
	d.RuntimeStack.Present = true
	d.RuntimeStack.Schema, d.RuntimeStack.Digest, d.RuntimeStack.WrittenBy = stamp.Schema, stamp.Digest, stamp.WrittenBy
	return nil
}

func broughtVarsKey(outputs map[string]string) string {
	if outputs[outputVarsKeyBrought] != "true" {
		return ""
	}
	return outputs[outputVarsKeyARN]
}

func standingFeatures(ctx context.Context, api cfn.StacksAPI, ns Namespace, class string) (FeatureSet, error) {
	standing := FeatureSet{}
	for _, f := range featureRegistry {
		stack, err := cfn.DescribeStack(ctx, api, f.stackName(ns, class))
		if err != nil {
			return nil, err
		}
		if stack != nil {
			standing[f.name] = true
		}
	}
	return standing, nil
}

func absorb(d *Deployed, refs *stackRefs, out map[string]string) error {
	layers := runtimeLayerArches()
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
		default:
			if arch, carried := layers[key]; carried {
				if d.RuntimeLayers == nil {
					d.RuntimeLayers = map[string]string{}
				}
				d.RuntimeLayers[arch] = value
			}
		}
	}
	return nil
}

type stackPayloads struct {
	optimizer   payloads.Placement
	publisher   payloads.Placement
	invalidator payloads.Placement
	revalidator payloads.Placement
}

type spec struct {
	ns        Namespace
	class     string
	stackName string
	stackStep string
}

func (s spec) core(broughtKey string) string { return coreStackTemplate(s.ns, s.class, broughtKey) }

func productionBootstrap(ns Namespace) spec {
	return spec{
		ns:        ns,
		class:     ClassProduction,
		stackName: ns.CoreStackName(),
		stackStep: "Ensuring Pulumi state bucket and state table (CloudFormation)",
	}
}

func previewBootstrap(ns Namespace) spec {
	name, _ := ns.StackNameFor(ClassPreview)
	return spec{
		ns:        ns,
		class:     ClassPreview,
		stackName: name,
		stackStep: "Ensuring preview infrastructure (CloudFormation)",
	}
}

func bootstrapFor(ns Namespace, class string) (spec, error) {
	switch class {
	case ClassProduction:
		return productionBootstrap(ns), nil
	case ClassPreview:
		return previewBootstrap(ns), nil
	default:
		return spec{}, fmt.Errorf("bootstrap: unknown class %q", class)
	}
}

func Run(ctx context.Context, apis APIs, ns Namespace, class string, req Request, progress, log func(string)) error {
	target, err := specFor(ns, class)
	if err != nil {
		return err
	}
	return run(ctx, apis, target, req, progress, log)
}

func specFor(ns Namespace, class string) (spec, error) {
	switch class {
	case ClassProduction:
		return productionBootstrap(ns), nil
	case ClassPreview:
		return previewBootstrap(ns), nil
	default:
		return spec{}, fmt.Errorf("there is no %s bootstrap; a bootstrap is either production or preview", class)
	}
}

func run(ctx context.Context, apis APIs, target spec, req Request, progress, log func(string)) error {
	var reporting sync.Mutex
	say := func(f func(string), msg string) {
		if f == nil {
			return
		}
		reporting.Lock()
		defer reporting.Unlock()
		f(msg)
	}
	progressf := func(msg string) { say(progress, msg) }
	logf := func(msg string) { say(log, msg) }

	requested := req.Features
	levels, err := featureLevels(requested)
	if err != nil {
		return err
	}

	progressf("Ensuring the secret the origin's own front authenticates with (SSM SecureString)")
	secret, err := ensureOriginSecret(ctx, apis.SSM, target.ns, target.class, time.Now())
	if err != nil {
		return err
	}
	if secret.retired {
		logf("retired the origin secret a rotation replaced; a release not re-deployed since no longer answers its front")
	}
	switch {
	case secret.rotated:
		logf(fmt.Sprintf("rotated the origin secret: it was older than %d days; re-deploy each project in the class so its releases accept the new one, and the old one is retired by the next bootstrap after %d days", int(OriginSecretMaxAge.Hours()/24), int(OriginSecretGrace.Hours()/24)))
	case secret.minted:
		logf("minted a new origin secret")
	default:
		logf("reused the existing origin secret")
	}

	alongside := FeatureSet{}
	for _, name := range requested {
		alongside[name] = true
	}

	progressf(target.stackStep)
	namedIAM := []cfntypes.Capability{cfntypes.CapabilityCapabilityNamedIam}
	review := AdmitReplacements(target.ns, req.AcceptReplacements, logf)
	coreBody := target.core(coreVarsKey(alongside, req.VarsKey))
	coreTags := stampTags(target.ns, Stamp{Schema: RequiredSchema, Digest: cfn.TemplateDigest(coreBody), WrittenBy: req.Writer.String()})
	if err := cfn.Upsert(ctx, apis.CFN, target.ns.ChangeSetNameFor, target.stackName, coreBody, nil, namedIAM, coreTags, review); err != nil {
		return err
	}
	deployed, refs, err := readBootstrap(ctx, apis.CFN, target.ns, target.class)
	if err != nil {
		return err
	}
	if err := applyRuntimeLayers(ctx, apis, target, req, deployed.ArtifactBucket, progressf, logf); err != nil {
		return err
	}
	steps := stepDeps{ns: target.ns, class: target.class, ssm: apis.SSM, iam: apis.IAM, progress: progressf, log: logf}
	dropped := Removing(req.Features, req.Remove)
	if err := tearDownEdges(ctx, apis, steps, dropped); err != nil {
		return err
	}
	if err := bootstrapEdges(ctx, apis, steps, req.Features, dropped); err != nil {
		return err
	}

	progressf("Ensuring Pulumi passphrase (SSM SecureString)")
	created, err := ensurePassphrase(ctx, apis.SSM, target.ns)
	if err != nil {
		return err
	}
	if created {
		logf("generated a new Pulumi passphrase")
	} else {
		logf("reused the existing Pulumi passphrase")
	}

	if err := dropFeatures(ctx, apis.CFN, steps, req.Remove, progressf, logf); err != nil {
		return err
	}

	for _, level := range levels {
		progressf(fmt.Sprintf("Applying %s (CloudFormation)", strings.Join(featureStackNames(target.ns, level, target.class), ", ")))
		produced := make([]map[string]string, len(level))
		group, gctx := errgroup.WithContext(ctx)
		for i, name := range level {
			group.Go(func() error {
				f, _ := featureNamed(name)
				stackName := f.stackName(target.ns, target.class)
				stack, err := f.staged(gctx, apis.Store, featureInputs{
					ns:             target.ns,
					class:          target.class,
					artifactBucket: deployed.ArtifactBucket,
					refs:           refs,
					alongside:      alongside,
					varsKey:        req.VarsKey,
				})
				if err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				tags := stampTags(target.ns, Stamp{Schema: RequiredSchema, Digest: cfn.TemplateDigest(stack.body), WrittenBy: req.Writer.String()})
				if err := cfn.Upsert(gctx, apis.CFN, target.ns.ChangeSetNameFor, stackName, stack.body, stack.params, namedIAM, tags, review); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				if produced[i], err = cfn.StackOutputs(gctx, apis.CFN, stackName); err != nil {
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

func dropFeatures(ctx context.Context, stacks cfn.API, steps stepDeps, dropOrder []string, progressf, logf func(string)) error {
	if len(dropOrder) == 0 {
		return nil
	}
	progressf(fmt.Sprintf("Removing %s (CloudFormation)", strings.Join(featureStackNames(steps.ns, dropOrder, steps.class), ", ")))
	for _, name := range dropOrder {
		f, _ := featureNamed(name)
		if f.drop == nil {
			continue
		}
		if err := f.drop(ctx, steps); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return deleteFeatureStacks(ctx, stacks, steps.ns, steps.class, dropOrder, logf)
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
			if err := adoptCacheStore(ctx, d.ssm, d.ns, d.class, front.Kind(), offer.Values); err != nil {
				return err
			}
		case edge.OfferDeploymentsStore:
			d.progress("Adopting the deployments-store worker (SSM SecureString)")
			if err := adoptDeploymentsStore(ctx, d.ssm, d.ns, d.class, front.Kind(), offer.Values); err != nil {
				return err
			}
		case edge.OfferISRWriter:
			d.progress("Adopting the ISR writer worker (SSM SecureString)")
			if err := adoptISRWriter(ctx, d.ssm, d.ns, d.class, front.Kind(), offer.Values); err != nil {
				return err
			}
			if _, err := ensureISRWriterSeed(ctx, d.ssm, d.ns, d.class, front.Kind()); err != nil {
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
	return writeEdgeValues(ctx, d.ssm, d.ns, d.class, front.Kind(), out.Values)
}

func absorbRefs(refs *stackRefs, out map[string]string) error {
	var ignored Deployed
	return absorb(&ignored, refs, out)
}

func ensurePassphrase(ctx context.Context, ssmClient SSMAPI, ns Namespace) (created bool, err error) {
	_, err = ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(ns.PassphraseParamName()),
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
		Name:        aws.String(ns.PassphraseParamName()),
		Description: aws.String("Ocel: the passphrase every Pulumi stack in this account is encrypted under, production and preview alike. This is the only copy - delete it and that state can never be decrypted again."),
		Value:       aws.String(passphrase),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(false),
	}); err != nil {
		return false, fmt.Errorf("write passphrase parameter: %w", err)
	}
	return true, nil
}

func ReadPassphrase(ctx context.Context, ssmClient SSMAPI, ns Namespace) (string, error) {
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(ns.PassphraseParamName()),
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

func coreVarsKey(alongside FeatureSet, brought string) string {
	if alongside.Has(FeatureVarsKey) {
		return brought
	}
	return ""
}

func coreStackTemplate(ns Namespace, class, broughtKey string) string {
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
		stateBucketResource(class), stateTableResource(), artifactBucketResource(), assetBucketResource(), assetBucketPolicyResource(), varsResources(class), appBoundaryResource(ns, class, broughtKey),
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
