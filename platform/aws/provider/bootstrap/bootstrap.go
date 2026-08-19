package bootstrap

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"errors"
	"fmt"
	mathrand "math/rand/v2"
	"strconv"
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

	"github.com/ocelhq/ocel/platform/aws/provider/payloads"
	"github.com/ocelhq/ocel/platform/aws/provider/stackindex"
	edge "github.com/ocelhq/ocel/platform/edge/contract"
)

const (
	StackName = "ocel-bootstrap"

	PreviewStackName = "ocel-bootstrap-preview"

	PassphraseParamName = "/ocel/pulumi/passphrase"

	EdgeUserName        = "ocel-edge"
	EdgePreviewUserName = "ocel-edge-preview"

	StateTableIndexName = stackindex.IndexName

	outputStateBucket         = "StateBucketName"
	outputStateTable          = "StateTableName"
	outputStateTableARN       = "StateTableArn"
	outputStateTableStreamARN = "StateTableStreamArn"
	outputArtifactBucket      = "ArtifactBucketName"
	outputAssetBucket         = "AssetBucketName"
	outputAssetBucketARN      = "AssetBucketArn"
	outputVersion             = "BootstrapVersion"
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
	Version            int
	Features           FeatureSet
	StateBucket        string
	StateTable         string
	ArtifactBucket     string
	AssetBucket        string
	VarsTable          string
	VarsKeyARN         string
	ImageOptimizerURL  string
	RevalidateQueueURL string
	Class              string
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
}

type SSMAPI interface {
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
	PutParameter(ctx context.Context, in *ssm.PutParameterInput, optFns ...func(*ssm.Options)) (*ssm.PutParameterOutput, error)
	DeleteParameter(ctx context.Context, in *ssm.DeleteParameterInput, optFns ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error)
}

type FeatureUsers interface {
	ProjectFeatures(ctx context.Context) (map[string][]string, error)
}

type APIs struct {
	CFN   CFNAPI
	SSM   SSMAPI
	IAM   IAMAPI
	Store ObjectStore
	Users FeatureUsers
	Edge  edge.Edge
}

type Request struct {
	Features []string
	Force    bool
}

func CheckDeployed(ctx context.Context, api CFNDescriber) (Deployed, error) {
	deployed, _, err := readSubstrate(ctx, api, ClassProduction)
	return deployed, err
}

func CheckDeployedPreview(ctx context.Context, api CFNDescriber) (Deployed, error) {
	deployed, _, err := readSubstrate(ctx, api, ClassPreview)
	return deployed, err
}

func readSubstrate(ctx context.Context, api CFNDescriber, class string) (Deployed, stackRefs, error) {
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

	d := Deployed{Present: true, Features: FeatureSet{}}
	var refs stackRefs
	if err := absorb(&d, &refs, outputsOf(core)); err != nil {
		return Deployed{}, stackRefs{}, err
	}
	for _, f := range featureRegistry {
		stack, err := describeStack(ctx, api, f.stackName(class))
		if err != nil {
			return Deployed{}, stackRefs{}, err
		}
		if stack == nil || stackUnusable(stack.StackStatus) {
			continue
		}
		d.Features[f.name] = true
		if err := absorb(&d, &refs, outputsOf(stack)); err != nil {
			return Deployed{}, stackRefs{}, err
		}
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
		case outputInfraClass:
			d.Class = value
		case outputVersion:
			version, err := strconv.Atoi(value)
			if err != nil {
				return fmt.Errorf("invalid bootstrap version %q: %w", value, err)
			}
			if d.Version == 0 || version < d.Version {
				d.Version = version
			}
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

type substrate struct {
	class     string
	stackName string
	stackStep string
	template  func(int) string
}

func productionSubstrate() substrate {
	return substrate{
		class:     ClassProduction,
		stackName: StackName,
		stackStep: "Ensuring Pulumi state bucket and state table (CloudFormation)",
		template:  stackTemplate,
	}
}

func previewSubstrate() substrate {
	return substrate{
		class:     ClassPreview,
		stackName: PreviewStackName,
		stackStep: "Ensuring preview infrastructure (CloudFormation)",
		template:  previewStackTemplate,
	}
}

func Run(ctx context.Context, apis APIs, req Request, progress, log func(string)) error {
	return run(ctx, apis, productionSubstrate(), req, progress, log)
}

func RunPreview(ctx context.Context, apis APIs, req Request, progress, log func(string)) error {
	return run(ctx, apis, previewSubstrate(), req, progress, log)
}

func run(ctx context.Context, apis APIs, sub substrate, req Request, progress, log func(string)) error {
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

	requested, err := resolveFeatures(req.Features)
	if err != nil {
		return err
	}
	levels, err := featureLevels(requested)
	if err != nil {
		return err
	}

	standing, err := standingFeatures(ctx, apis.CFN, sub.class)
	if err != nil {
		return err
	}
	_, drop := featureDiff(standing, requested)
	drop = dropClosure(drop, standing)
	if err := admitDrops(ctx, apis.Users, drop, req.Force); err != nil {
		return err
	}

	progressf("Ensuring the secret the origin's own front authenticates with (SSM SecureString)")
	if _, err := ensureOriginSecret(ctx, apis.SSM, sub.class); err != nil {
		return err
	}

	progressf(sub.stackStep)
	namedIAM := []cfntypes.Capability{cfntypes.CapabilityCapabilityNamedIam}
	if err := upsertCFNStack(ctx, apis.CFN, sub.stackName, sub.template(RequiredBootstrapVersion), nil, namedIAM); err != nil {
		return err
	}
	deployed, refs, err := readSubstrate(ctx, apis.CFN, sub.class)
	if err != nil {
		return err
	}
	steps := stepDeps{class: sub.class, ssm: apis.SSM, iam: apis.IAM, progress: progressf, log: logf}
	if err := bootstrapEdge(ctx, steps, apis.Edge); err != nil {
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

	alongside := FeatureSet{}
	for _, name := range requested {
		alongside[name] = true
	}

	for _, level := range levels {
		for _, name := range level {
			f, _ := featureNamed(name)
			if f.before == nil {
				continue
			}
			if err := f.before(ctx, steps); err != nil {
				return fmt.Errorf("%s: %w", name, err)
			}
		}

		progressf(fmt.Sprintf("Applying %s (CloudFormation)", strings.Join(featureStackNames(level, sub.class), ", ")))
		produced := make([]map[string]string, len(level))
		group, gctx := errgroup.WithContext(ctx)
		for i, name := range level {
			group.Go(func() error {
				f, _ := featureNamed(name)
				stackName := f.stackName(sub.class)
				code, err := f.payloads(gctx, apis.Store, deployed.ArtifactBucket)
				if err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				stack := f.template(featureInputs{
					class:     sub.class,
					version:   RequiredBootstrapVersion,
					code:      code,
					refs:      refs,
					alongside: alongside,
				})
				if err := upsertCFNStack(gctx, apis.CFN, stackName, stack.body, stack.params, namedIAM); err != nil {
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

	if len(drop) == 0 {
		return nil
	}
	dropOrder, err := FeatureDeleteOrder(drop)
	if err != nil {
		return err
	}
	progressf(fmt.Sprintf("Removing %s (CloudFormation)", strings.Join(featureStackNames(dropOrder, sub.class), ", ")))
	for _, name := range dropOrder {
		f, _ := featureNamed(name)
		if f.drop == nil {
			continue
		}
		if err := f.drop(ctx, steps); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return deleteFeatureStacks(ctx, apis.CFN, sub.class, drop, logf)
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
			if err := adoptDeploymentsStore(ctx, d.ssm, d.class, offer.Values); err != nil {
				return err
			}
		case edge.OfferISRWriter:
			d.progress("Adopting the ISR writer worker (SSM SecureString)")
			if err := adoptISRWriter(ctx, d.ssm, d.class, offer.Values); err != nil {
				return err
			}
			if _, err := ensureISRWriterSeed(ctx, d.ssm, d.class); err != nil {
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
	return writeEdgeValues(ctx, d.ssm, d.class, out.Values)
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

func admitDrops(ctx context.Context, users FeatureUsers, drop []string, force bool) error {
	if len(drop) == 0 || force || users == nil {
		return nil
	}
	recorded, err := users.ProjectFeatures(ctx)
	if err != nil {
		return err
	}
	dependents := projectsDependingOn(recorded, drop)
	if len(dependents) == 0 {
		return nil
	}
	return fmt.Errorf(
		"dropping %s would break %d project(s) already deployed here: %s — re-run with --force to drop it anyway, or leave the feature in the set",
		strings.Join(drop, ", "), len(dependents), strings.Join(dependents, ", "),
	)
}

func upsertCFNStack(ctx context.Context, cfn CFNAPI, stackName, template string, params []cfntypes.Parameter, capabilities []cfntypes.Capability) error {
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
		return createCFNStack(ctx, cfn, stackName, template, params, capabilities)
	}

	if _, err := cfn.UpdateStack(ctx, &cloudformation.UpdateStackInput{
		StackName:    aws.String(stackName),
		TemplateBody: aws.String(template),
		Parameters:   params,
		Capabilities: capabilities,
	}); err != nil {
		if isNoUpdates(err) {
			return nil
		}
		return fmt.Errorf("update %s stack: %w", stackName, err)
	}
	w := cloudformation.NewStackUpdateCompleteWaiter(cfn)
	if err := w.Wait(ctx, &cloudformation.DescribeStacksInput{StackName: aws.String(stackName)}, stackWaitTimeout); err != nil {
		return fmt.Errorf("wait for %s update: %w", stackName, err)
	}
	return nil
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

func createCFNStack(ctx context.Context, cfn CFNAPI, stackName, template string, params []cfntypes.Parameter, capabilities []cfntypes.Capability) error {
	for attempt := 0; ; attempt++ {
		err := createOnce(ctx, cfn, stackName, template, params, capabilities)
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

func createOnce(ctx context.Context, cfn CFNAPI, stackName, template string, params []cfntypes.Parameter, capabilities []cfntypes.Capability) error {
	if _, err := cfn.CreateStack(ctx, &cloudformation.CreateStackInput{
		StackName:    aws.String(stackName),
		TemplateBody: aws.String(template),
		Parameters:   params,
		Capabilities: capabilities,
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
		Description: aws.String("Ocel: the passphrase every Pulumi stack in this account is encrypted under, production and preview alike. Generated once by ocel bootstrap and never rotated. It is the only copy - delete it and the state in the Pulumi state buckets can never be decrypted again, stranding every app Ocel has deployed here."),
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

func stackTemplate(version int) string {
	return fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: "Ocel bootstrap (production) - the account-global core every production app Ocel deploys into this AWS account is built on: the Pulumi state bucket and state table, the artifact and asset buckets and the variable store. Created and updated by ocel bootstrap; each optional feature it can carry is a stack of its own beside this one. It holds no app of its own. Deleting this stack orphans every app deployed from it: the Pulumi state describing them goes with its bucket, and no deploy or teardown can run until it is recreated."
Resources:
%s%s%s%s%s%sOutputs:
  %s:
    Description: "S3 bucket holding the Pulumi state Ocel plans every production deploy and teardown from. One versioned object per app stack."
    Value: !Ref StateBucket
%s%s%s%s  %s:
    Description: "Schema version of this bootstrap stack. The CLI refuses to act while its required version and this one disagree, and points at the side that has to move."
    Value: '%d'
  %s:
    Description: "Class this substrate is stamped with, verified before an action runs so that a preview deploy cannot reach production state, variables or caches."
    Value: '%s'
`, stateBucketResource(ClassProduction), stateTableResource(), artifactBucketResource(), assetBucketResource(), assetBucketPolicyResource(), varsResources(ClassProduction), outputStateBucket, stateTableOutputs(), artifactBucketOutput(), assetBucketOutputs(), varsOutputs(), outputVersion, version, outputInfraClass, ClassProduction)
}

func previewStackTemplate(version int) string {
	return fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: "Ocel bootstrap (preview) - the account-global core every preview environment Ocel deploys into this AWS account is carved from, deliberately separate from the production bootstrap so a per-PR preview can never reach production state, variables or caches. Created and updated by ocel bootstrap --preview; each optional feature it can carry is a stack of its own beside this one. Deleting this stack orphans every live preview: the Pulumi state describing them goes with its bucket, and no preview deploy or teardown can run until it is recreated."
Resources:
%s%s%s%s%s%sOutputs:
  %s:
    Description: "S3 bucket holding the Pulumi state Ocel plans every preview deploy and teardown from. One versioned object per preview stack."
    Value: !Ref StateBucket
%s%s%s%s  %s:
    Description: "Schema version of this bootstrap stack. The CLI refuses to act while its required version and this one disagree, and points at the side that has to move."
    Value: '%d'
  %s:
    Description: "Class this substrate is stamped with, verified before an action runs so that a preview deploy cannot reach production state, variables or caches."
    Value: '%s'
`, stateBucketResource(ClassPreview), stateTableResource(), artifactBucketResource(), assetBucketResource(), assetBucketPolicyResource(), varsResources(ClassPreview), outputStateBucket, stateTableOutputs(), artifactBucketOutput(), assetBucketOutputs(), varsOutputs(), outputVersion, version, outputInfraClass, ClassPreview)
}

func stateBucketResource(class string) string {
	scope := "production app"
	if class == ClassPreview {
		scope = "preview environment"
	}
	return fmt.Sprintf(`  StateBucket:
    Type: AWS::S3::Bucket
    Metadata:
      Description: "Pulumi state for every %s Ocel has deployed into this account, one versioned object per stack. Ocel reads it to plan a deploy and to tear one down, so emptying or deleting this bucket strands what is already deployed: the infrastructure keeps running and Ocel can no longer describe, update or remove it."
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
`, scope, stateNoncurrentDays, stateAbortMultipartDays)
}

func stateTableResource() string {
	return fmt.Sprintf(`  StateTable:
    Type: AWS::DynamoDB::Table
    Metadata:
      Description: "Account-global Ocel state, keyed by pk/sk: the index of every stack this substrate has deployed, which prune and teardown walk, and the tag clock the edge reads and updates to decide whether a cached page is still fresh. Deleting it loses both - Ocel forgets what it deployed here, and every tag's invalidation history starts over."
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
    Description: "DynamoDB table holding account-global Ocel state keyed by pk/sk: the stack index prune and teardown walk, and the ISR tag clock every app in this substrate shares with the edge."
    Value: !Ref StateTable
  %s:
    Description: "ARN of that table, handed to every feature stack that has to grant an item read or write on it."
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
      Description: "Staging area for the Lambda code Ocel uploads before a stack references it. A deployed function holds its own copy and these objects age out on a lifecycle rule, so deleting this bucket costs the next bootstrap and the next deploy a re-upload, not a running app."
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
    Description: "S3 bucket Ocel stages function code in before a stack references it. Objects age out on a lifecycle rule; a deployed function keeps its own copy."
    Value: !Ref ArtifactBucket
`, outputArtifactBucket)
}

func assetBucketResource() string {
	return fmt.Sprintf(`  AssetBucket:
    Type: AWS::S3::Bucket
    Metadata:
      Description: "Per-build static assets, prerender fallbacks, image-optimizer config and the edge's fetch cache, keyed by build id. The edge, the image optimizer, the tag publisher and the revalidator all read it directly, so deleting it breaks static assets and image optimization for every app in this substrate until each is redeployed."
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
      Description: "The only grant on the asset bucket. It lets CloudFront distributions in this account, and no one else, read objects out of it over an origin access control; the bucket itself stays closed to the public. One statement for the whole account, so no deploy ever rewrites it and concurrent deploys cannot clobber each other."
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
    Description: "S3 bucket holding per-build static assets, prerender fallbacks, image-optimizer config and the edge fetch cache, keyed by build id. Read directly by the edge."
    Value: !Ref AssetBucket
  %s:
    Description: "ARN of that bucket, handed to every feature stack that has to grant a read or a write inside it."
    Value: !GetAtt AssetBucket.Arn
`, outputAssetBucket, outputAssetBucketARN)
}

func isStackNotFound(err error) bool {
	return isValidationErrorContaining(err, "does not exist")
}

func isNoUpdates(err error) bool {
	return isValidationErrorContaining(err, "No updates are to be performed")
}

func isValidationErrorContaining(err error, substr string) bool {
	var apiErr smithy.APIError
	if !errors.As(err, &apiErr) {
		return false
	}
	return apiErr.ErrorCode() == "ValidationError" && strings.Contains(apiErr.ErrorMessage(), substr)
}

const stackWaitTimeout = 10 * time.Minute
