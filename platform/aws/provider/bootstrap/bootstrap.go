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
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	cfntypes "github.com/aws/aws-sdk-go-v2/service/cloudformation/types"
	"github.com/aws/aws-sdk-go-v2/service/ssm"
	ssmtypes "github.com/aws/aws-sdk-go-v2/service/ssm/types"
	"golang.org/x/sync/errgroup"

	"github.com/ocelhq/ocel/pkg/edge"
	"github.com/ocelhq/ocel/pkg/environment"
	"github.com/ocelhq/ocel/pkg/progress"
	"github.com/ocelhq/ocel/pkg/provider"
	"github.com/ocelhq/ocel/platform/aws/provider/cfn"
	"github.com/ocelhq/ocel/platform/aws/provider/tagclock"
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
	outputInfraTier           = "InfrastructureTier"

	artifactExpirationDays     = 30
	artifactAbortMultipartDays = 7

	stateNoncurrentDays     = 7
	stateAbortMultipartDays = 7
)

type Deployed struct {
	Present           bool
	Stacks            []StackStamp
	Parts             PartSet
	StateBucket       string
	StateTable        string
	ArtifactBucket    string
	AssetBucket       string
	VariablesTable    string
	VariablesTableARN string
	VariablesKeyARN   string
	AppBoundaryARN    string

	StateTableStreamARN   string
	TagInvalidatorRoleARN string
	EdgeInvokeRoleARN     string

	Tier    environment.Tier
	Outputs map[string]string
}

type SSMAPI interface {
	SSMBatchAPI
	GetParameter(ctx context.Context, in *ssm.GetParameterInput, optFns ...func(*ssm.Options)) (*ssm.GetParameterOutput, error)
	PutParameter(ctx context.Context, in *ssm.PutParameterInput, optFns ...func(*ssm.Options)) (*ssm.PutParameterOutput, error)
	DeleteParameter(ctx context.Context, in *ssm.DeleteParameterInput, optFns ...func(*ssm.Options)) (*ssm.DeleteParameterOutput, error)
}

type APIs struct {
	CFN     cfn.API
	SSM     SSMAPI
	IAM     IAMAPI
	Layers  LayerAPI
	Store   ObjectStore
	Records RecordStore
	Edge    edge.Edge
	Edges   provider.Edges
}

type Request struct {
	VariablesKey       string
	Parts              []string
	Remove             []string
	Writer             provider.WrittenBy
	AcceptReplacements bool
}

func CheckDeployed(ctx context.Context, api cfn.StacksAPI, ns Namespace) (Deployed, error) {
	return CheckDeployedFor(ctx, api, ns, environment.TierProduction)
}

func CheckDeployedPreview(ctx context.Context, api cfn.StacksAPI, ns Namespace) (Deployed, error) {
	return CheckDeployedFor(ctx, api, ns, environment.TierPreview)
}

func CheckDeployedFor(ctx context.Context, api cfn.StacksAPI, ns Namespace, tier environment.Tier) (Deployed, error) {
	deployed, _, err := readBootstrap(ctx, api, ns, tier)
	return deployed, err
}

type Reading struct {
	Deployed Deployed
	ns       Namespace
	tier     environment.Tier
	refs     stackRefs
}

func (r Reading) Tier() environment.Tier { return r.tier }

func (r Reading) Namespace() Namespace { return r.ns }

func Read(ctx context.Context, api cfn.StacksAPI, ns Namespace, tier environment.Tier) (Reading, error) {
	deployed, refs, err := readBootstrap(ctx, api, ns, tier)
	if err != nil {
		return Reading{}, err
	}
	return Reading{Deployed: deployed, ns: ns, tier: tier, refs: refs}, nil
}

func readBootstrap(ctx context.Context, api cfn.StacksAPI, ns Namespace, tier environment.Tier) (Deployed, stackRefs, error) {
	coreStack, err := ns.StackNameFor(tier)
	if err != nil {
		return Deployed{}, stackRefs{}, err
	}
	core, err := cfn.DescribeStack(ctx, api, coreStack)
	if err != nil {
		return Deployed{}, stackRefs{}, err
	}
	if core == nil || cfn.Unusable(core.StackStatus) {
		return Deployed{Present: false, Parts: PartSet{}}, stackRefs{}, nil
	}

	d := Deployed{Present: true, Parts: PartSet{}, Outputs: cfn.OutputsOf(core)}
	var refs stackRefs
	if err := absorb(&d, &refs, d.Outputs); err != nil {
		return Deployed{}, stackRefs{}, err
	}
	coreStamp := readStamp(core.Tags)

	stamps := make(map[string]Stamp, len(partRegistry))
	for _, f := range partRegistry {
		stack, err := cfn.DescribeStack(ctx, api, f.stackName(ns, tier))
		if err != nil {
			return Deployed{}, stackRefs{}, err
		}
		if stack == nil || cfn.Unusable(stack.StackStatus) {
			continue
		}
		d.Parts[f.name] = true
		stamps[f.name] = readStamp(stack.Tags)
		out := cfn.OutputsOf(stack)
		maps.Copy(d.Outputs, out)
		if err := absorb(&d, &refs, out); err != nil {
			return Deployed{}, stackRefs{}, err
		}
	}

	target, err := bootstrapFor(ns, tier)
	if err != nil {
		return Deployed{}, stackRefs{}, err
	}
	d.Stacks = append(d.Stacks, StackStamp{
		Name:      target.stackName,
		Present:   true,
		Digest:    coreStamp.Digest,
		Intended:  cfn.TemplateDigest(target.core(broughtVariablesKey(d.Outputs))),
		WrittenBy: coreStamp.WrittenBy,
	})
	for _, f := range partRegistry {
		if !d.Parts.Has(f.name) {
			d.Stacks = append(d.Stacks, StackStamp{Name: f.stackName(ns, tier), Part: f.name})
			continue
		}
		stamp := stamps[f.name]
		intended, err := f.template(partInputs{
			ns:           ns,
			tier:         tier,
			refs:         refs,
			alongside:    d.Parts,
			variablesKey: broughtVariablesKey(d.Outputs),
		})
		if err != nil {
			return Deployed{}, stackRefs{}, err
		}
		d.Stacks = append(d.Stacks, StackStamp{
			Name:      f.stackName(ns, tier),
			Part:      f.name,
			Present:   true,
			Digest:    stamp.Digest,
			Intended:  cfn.TemplateDigest(intended.body),
			WrittenBy: stamp.WrittenBy,
		})
	}
	return d, refs, nil
}

func broughtVariablesKey(outputs map[string]string) string {
	if outputs[outputVariablesKeyBrought] != "true" {
		return ""
	}
	return outputs[outputVariablesKeyARN]
}

func installedParts(ctx context.Context, api cfn.StacksAPI, ns Namespace, tier environment.Tier) (PartSet, error) {
	installed := PartSet{}
	for _, f := range partRegistry {
		stack, err := cfn.DescribeStack(ctx, api, f.stackName(ns, tier))
		if err != nil {
			return nil, err
		}
		if stack != nil {
			installed[f.name] = true
		}
	}
	return installed, nil
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
			d.StateTableStreamARN, refs.stateTableStreamARN = value, value
		case outputTagInvalidatorRoleARN:
			d.TagInvalidatorRoleARN = value
		case OutputEdgeInvokeRoleARN:
			d.EdgeInvokeRoleARN = value
		case outputArtifactBucket:
			d.ArtifactBucket = value
		case outputAssetBucket:
			d.AssetBucket, refs.assetBucket = value, value
		case outputAssetBucketARN:
			refs.assetBucketARN = value
		case outputVariablesTable:
			d.VariablesTable = value
		case outputVariablesTableARN:
			d.VariablesTableARN = value
		case outputVariablesKeyARN:
			d.VariablesKeyARN = value
		case outputAppBoundaryARN:
			d.AppBoundaryARN = value
		case outputInfraTier:
			d.Tier = environment.Tier(value)
		}
	}
	return nil
}

type spec struct {
	ns        Namespace
	tier      environment.Tier
	stackName string
	stackStep string
}

func (s spec) core(broughtKey string) string { return coreStackTemplate(s.ns, s.tier, broughtKey) }

func productionBootstrap(ns Namespace) spec {
	return spec{
		ns:        ns,
		tier:      environment.TierProduction,
		stackName: ns.CoreStackName(),
		stackStep: "Ensuring Pulumi state bucket and state table (CloudFormation)",
	}
}

func previewBootstrap(ns Namespace) spec {
	name, _ := ns.StackNameFor(environment.TierPreview)
	return spec{
		ns:        ns,
		tier:      environment.TierPreview,
		stackName: name,
		stackStep: "Ensuring preview infrastructure (CloudFormation)",
	}
}

func bootstrapFor(ns Namespace, tier environment.Tier) (spec, error) {
	switch tier {
	case environment.TierProduction:
		return productionBootstrap(ns), nil
	case environment.TierPreview:
		return previewBootstrap(ns), nil
	default:
		return spec{}, fmt.Errorf("bootstrap: unknown tier %q", tier)
	}
}

func Run(ctx context.Context, apis APIs, ns Namespace, tier environment.Tier, req Request, progress progress.Log) error {
	target, err := specFor(ns, tier)
	if err != nil {
		return err
	}
	return run(ctx, apis, target, req, ensureProgress(progress))
}

func ensureProgress(runProgress progress.Log) progress.Log {
	if runProgress == nil {
		return progress.Discard()
	}
	return runProgress
}

func specFor(ns Namespace, tier environment.Tier) (spec, error) {
	switch tier {
	case environment.TierProduction:
		return productionBootstrap(ns), nil
	case environment.TierPreview:
		return previewBootstrap(ns), nil
	default:
		return spec{}, fmt.Errorf("there is no %s bootstrap; a bootstrap is either production or preview", tier)
	}
}

func run(ctx context.Context, apis APIs, target spec, req Request, progress progress.Log) error {
	requested := req.Parts

	progress.Say("Ensuring the secret the origin's own front authenticates with (SSM SecureString)")
	secret, err := ensureOriginSecret(ctx, apis.SSM, target.ns, target.tier, time.Now())
	if err != nil {
		return err
	}
	if secret.retired {
		progress.Warn(fmt.Sprintf("Retired the %s origin secret a rotation replaced: a release not re-deployed since no longer answers its front", target.tier))
	}
	switch {
	case secret.rotated:
		progress.Warn(fmt.Sprintf("Rotated the %s origin secret, which was older than %d days: re-deploy each project in the tier so its releases accept the new one; the first bootstrap after %d days retires the old one", target.tier, int(OriginSecretMaxAge.Hours()/24), int(OriginSecretGrace.Hours()/24)))
	case secret.minted:
		progress.Say(fmt.Sprintf("Minted a new %s origin secret", target.tier))
	default:
		progress.Debug(fmt.Sprintf("Reused the existing %s origin secret", target.tier))
	}

	alongside := PartSet{}
	for _, name := range requested {
		alongside[name] = true
	}

	progress.Say(target.stackStep)
	namedIAM := []cfntypes.Capability{cfntypes.CapabilityCapabilityNamedIam}
	review := AdmitReplacements(target.ns, req.AcceptReplacements, progress)
	coreBody := target.core(coreVariablesKey(alongside, req.VariablesKey))
	coreTags := stampTags(target.ns, target.tier, Stamp{Digest: cfn.TemplateDigest(coreBody), WrittenBy: req.Writer.String()})
	if err := cfn.Upsert(ctx, apis.CFN, target.ns.ChangeSetNameFor, target.stackName, coreBody, nil, namedIAM, coreTags, review); err != nil {
		return err
	}
	deployed, refs, err := readBootstrap(ctx, apis.CFN, target.ns, target.tier)
	if err != nil {
		return err
	}
	progress.Say(runtimeLayerStep)
	if _, err := EnsureRuntimeLayers(ctx, apis, target.ns, target.tier, RuntimeLayerRequest{ArtifactBucket: deployed.ArtifactBucket, StateTable: deployed.StateTable}, progress); err != nil {
		return err
	}
	steps := stepDeps{ns: target.ns, tier: target.tier, ssm: apis.SSM, iam: apis.IAM, progress: progress}
	dropped := Removing(req.Parts, req.Remove)
	if err := tearDownEdges(ctx, apis, steps, dropped); err != nil {
		return err
	}
	if err := bootstrapEdges(ctx, apis, steps, req.Parts, dropped); err != nil {
		return err
	}

	progress.Say("Ensuring the Pulumi passphrase (SSM SecureString)")
	created, err := ensurePassphrase(ctx, apis.SSM, target.ns, target.tier)
	if err != nil {
		return err
	}
	if created {
		progress.Say("Generated a new Pulumi passphrase")
	} else {
		progress.Debug("Reused the existing Pulumi passphrase")
	}

	if err := dropParts(ctx, apis.CFN, steps, req.Remove); err != nil {
		return err
	}

	if len(requested) > 0 {
		progress.Say(fmt.Sprintf("Applying %s (CloudFormation)", strings.Join(partStackNames(target.ns, requested, target.tier), ", ")))
		produced := make([]map[string]string, len(requested))
		group, gctx := errgroup.WithContext(ctx)
		for i, name := range requested {
			group.Go(func() error {
				f, _ := partNamed(name)
				stackName := f.stackName(target.ns, target.tier)
				stack, err := f.template(partInputs{
					ns:           target.ns,
					tier:         target.tier,
					refs:         refs,
					alongside:    alongside,
					variablesKey: req.VariablesKey,
				})
				if err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				tags := stampTags(target.ns, target.tier, Stamp{Digest: cfn.TemplateDigest(stack.body), WrittenBy: req.Writer.String()})
				if err := cfn.Upsert(gctx, apis.CFN, target.ns.ChangeSetNameFor, stackName, stack.body, stack.params, namedIAM, tags, review); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				if produced[i], err = cfn.StackOutputs(gctx, apis.CFN, stackName); err != nil {
					return fmt.Errorf("%s: %w", name, err)
				}
				progress.Say("Applied stack " + stackName)
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

		for _, name := range requested {
			f, _ := partNamed(name)
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

func dropParts(ctx context.Context, stacks cfn.API, steps stepDeps, dropOrder []string) error {
	if len(dropOrder) == 0 {
		return nil
	}
	steps.progress.Say(fmt.Sprintf("Removing %s (CloudFormation)", strings.Join(partStackNames(steps.ns, dropOrder, steps.tier), ", ")))
	for _, name := range dropOrder {
		f, _ := partNamed(name)
		if f.drop == nil {
			continue
		}
		if err := f.drop(ctx, steps); err != nil {
			return fmt.Errorf("%s: %w", name, err)
		}
	}
	return deletePartStacks(ctx, stacks, steps.ns, steps.tier, dropOrder, steps.progress)
}

func openEdge(apis APIs, kind edge.Kind) (edge.Edge, error) {
	if apis.Edge != nil && apis.Edge.Kind() == kind {
		return apis.Edge, nil
	}
	if apis.Edges == nil {
		return nil, fmt.Errorf("bootstrap: this run has no edge registry, so it cannot reach the %s edge its parts depend on", kind)
	}
	return apis.Edges.Open(kind, nil)
}

func bootstrapEdges(ctx context.Context, apis APIs, d stepDeps, parts, dropped []string) error {
	torn := EdgeKindsFor(dropped)
	if !slices.Contains(torn, apis.Edge.Kind()) {
		if err := bootstrapEdge(ctx, d, apis.Edge); err != nil {
			return err
		}
	}
	for _, kind := range EdgeKindsFor(parts) {
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
	d.progress.Say(fmt.Sprintf("Tearing the %s edge down", front.Kind()))
	if err := front.Teardown(ctx, d.tier); err != nil {
		return fmt.Errorf("tear down %s edge: %w", front.Kind(), err)
	}
	return nil
}

func bootstrapEdge(ctx context.Context, d stepDeps, front edge.Edge) error {
	d.progress.Say(fmt.Sprintf("Bootstrapping the %s edge", front.Kind()))
	out, err := front.Bootstrap(ctx, d.tier)
	if err != nil {
		return fmt.Errorf("bootstrap %s edge: %w", front.Kind(), err)
	}
	for _, offer := range out.Offers {
		switch offer.Kind {
		case edge.OfferReleasesStore:
			d.progress.Say(fmt.Sprintf("Adopting the %s edge's releases-store worker (SSM SecureString)", front.Kind()))
			if err := adoptReleasesStore(ctx, d.ssm, d.ns, d.tier, front.Kind(), offer.Values); err != nil {
				return err
			}
			if _, err := ensurePurgeSeed(ctx, d.ssm, d.ns, d.tier, front.Kind()); err != nil {
				return err
			}
		case edge.OfferWorkerClientCertificate:
		default:
			d.progress.Warn(fmt.Sprintf("Ignoring the %s edge's %q offer: nothing in this bootstrap adopts it", front.Kind(), offer.Kind))
		}
	}
	return nil
}

func absorbRefs(refs *stackRefs, out map[string]string) error {
	var ignored Deployed
	return absorb(&ignored, refs, out)
}

func ensurePassphrase(ctx context.Context, ssmClient SSMAPI, ns Namespace, tier environment.Tier) (created bool, err error) {
	_, err = ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(ns.PassphraseParamFor(tier)),
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
		Name:        aws.String(ns.PassphraseParamFor(tier)),
		Description: aws.String("Ocel: the passphrase every " + string(tier) + " Pulumi stack in this account is encrypted under. This is the only copy - delete it and that state can never be decrypted again."),
		Value:       aws.String(passphrase),
		Type:        ssmtypes.ParameterTypeSecureString,
		Overwrite:   aws.Bool(false),
	}); err != nil {
		return false, fmt.Errorf("write passphrase parameter: %w", err)
	}
	return true, nil
}

func ReadPassphrase(ctx context.Context, ssmClient SSMAPI, ns Namespace, tier environment.Tier) (string, error) {
	out, err := ssmClient.GetParameter(ctx, &ssm.GetParameterInput{
		Name:           aws.String(ns.PassphraseParamFor(tier)),
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

func coreVariablesKey(alongside PartSet, brought string) string {
	if alongside.Has(provider.VariablesKeyPart) {
		return brought
	}
	return ""
}

func coreStackTemplate(ns Namespace, tier environment.Tier, broughtKey string) string {
	return fmt.Sprintf(`AWSTemplateFormatVersion: '2010-09-09'
Description: %q
Resources:
%s%s%s%s%s%s%s%s%sOutputs:
  %s:
    Description: "S3 bucket storing the Pulumi state Ocel plans every %s deploy and teardown from. One versioned object per %s stack."
    Value: !Ref StateBucket
%s%s%s%s%s%s  %s:
    Description: "The tier this bootstrap was installed for: production or preview."
    Value: '%s'
`, coreStackDescription(tier),
		stateBucketResource(tier), stateTableResource(), artifactBucketResource(), assetBucketResource(), assetBucketPolicyResource(), variablesResources(tier), appBoundaryResource(ns, tier, broughtKey),
		tagInvalidatorRoleResource(ns, tier), edgeInvokeRoleResource(ns, tier),
		outputStateBucket, tier, scopeOf(tier),
		stateTableOutputs(), artifactBucketOutput(), assetBucketOutputs(), variablesOutputs(), appBoundaryOutput(), baseRoleOutputs(), outputInfraTier, tier)
}

func coreStackDescription(tier environment.Tier) string {
	apart := ""
	if tier == environment.TierPreview {
		apart = " It is kept apart from the production bootstrap so a per-PR preview never reaches production state, variables or caches."
	}
	return fmt.Sprintf("Ocel bootstrap (%s) - the account-global core every %s Ocel deploys here is built on: the Pulumi state bucket and state table, the artifact and asset buckets, the variable store and the roles the tag invalidator and API Gateway run as. Each other piece a deploy needs is added beside it on first use.%s", tier, scopeOf(tier), apart)
}

func scopeOf(tier environment.Tier) string {
	if tier == environment.TierPreview {
		return "preview environment"
	}
	return "production app"
}

func stateBucketResource(tier environment.Tier) string {
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
`, scopeOf(tier), stateNoncurrentDays, stateAbortMultipartDays)
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
    Description: "DynamoDB table storing account-global Ocel state: the stack index prune and teardown walk, and the ISR tag clock the edge shares."
    Value: !Ref StateTable
  %s:
    Description: "ARN of that table, handed to every part stack that grants an item read or write on it."
    Value: !GetAtt StateTable.Arn
  %s:
    Description: "ARN of that table's stream, handed to every part stack that reads tag writes off it."
    Value: !GetAtt StateTable.StreamArn
`, outputStateTable, outputStateTableARN, outputStateTableStreamARN)
}

func artifactBucketResource() string {
	return fmt.Sprintf(`  ArtifactBucket:
    Type: AWS::S3::Bucket
    Metadata:
      Description: "Staging area for the Lambda code Ocel uploads before a stack references it. A deployed function keeps its own copy, and these objects age out on a lifecycle rule."
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
      Description: "Per-build static assets, prerender fallbacks, image-optimizer config and the edge's fetch cache, keyed by build id. Read directly by the edge, the optimizer and the publisher."
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
    Description: "S3 bucket storing per-build static assets, prerender fallbacks, image-optimizer config and the edge fetch cache. Read directly by the edge."
    Value: !Ref AssetBucket
  %s:
    Description: "ARN of that bucket, handed to every part stack that grants a read or a write inside it."
    Value: !GetAtt AssetBucket.Arn
`, outputAssetBucket, outputAssetBucketARN)
}
